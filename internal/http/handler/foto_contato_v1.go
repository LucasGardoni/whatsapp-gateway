package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// A foto de perfil vem da CDN do WhatsApp com link que expira, entao o
// arquivo baixado e que fica guardado, e por pouco tempo: o contato troca a
// foto. "Sem foto" tambem fica guardado, senao a lista inteira pergunta de
// novo a z-api a cada atualizacao da tela.
const (
	ttlFotoContato     = 24 * time.Hour
	ttlSemFotoContato  = 12 * time.Hour
	ttlErroFotoContato = 15 * time.Minute
	tamanhoMaximoFoto  = 2 << 20
	timeoutFotoContato = 20 * time.Second
	// fotosSimultaneas segura a primeira carga da lista, que pede a foto de
	// todas as conversas de uma vez.
	fotosSimultaneas = 3
)

var errSemFoto = errors.New("contato sem foto")

// fotosContato liga GET /v1/conversas/{id}/foto (ComFotos).
type fotosContato struct {
	caixas    caixasV1
	provedor  func(caixa.Caixa) provedorFoto
	diretorio string
	http      *http.Client
	vagas     chan struct{}
}

type provedorFoto interface {
	FotoPerfil(ctx context.Context, phone string) (string, error)
}

// ComFotos liga a foto de perfil, guardada em <midiaDir>/fotos. Sem midiaDir
// ou sem ComFotos a rota responde 404, que a aplicacao trata como sem foto.
func (h *ConversasV1) ComFotos(caixas *caixa.Registro) *ConversasV1 {
	if h.midiaDir == "" {
		return h
	}
	h.fotos = &fotosContato{
		caixas: caixas,
		provedor: func(c caixa.Caixa) provedorFoto {
			if cl, ok := caixas.ZAPI(c); ok {
				return cl
			}
			return nil
		},
		diretorio: filepath.Join(h.midiaDir, "fotos"),
		http:      &http.Client{Timeout: timeoutFotoContato},
		vagas:     make(chan struct{}, fotosSimultaneas),
	}
	return h
}

// Foto atende GET /v1/conversas/{id}/foto: a foto de perfil do contato (ou do
// grupo) da conversa. 404 = sem foto, escondida pela privacidade ou provedor
// fora do ar.
func (h *ConversasV1) Foto(w http.ResponseWriter, r *http.Request) {
	conversaID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || conversaID <= 0 {
		http.Error(w, "id de conversa invalido", http.StatusBadRequest)
		return
	}
	if h.fotos == nil {
		http.Error(w, "foto nao configurada", http.StatusNotFound)
		return
	}

	conversa, err := store.New(h.pool).BuscarConversaComContatoPorID(r.Context(), conversaID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversa nao encontrada", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("conversas v1: buscar conversa da foto", "conversa_id", conversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	caminho, modificado, err := h.fotos.arquivo(r.Context(), conversa)
	if errors.Is(err, errSemFoto) {
		http.Error(w, "sem foto", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Warn("conversas v1: foto do contato", "conversa_id", conversaID, "erro", err)
		http.Error(w, "foto indisponivel", http.StatusNotFound)
		return
	}
	arquivo, err := os.Open(caminho)
	if err != nil {
		http.Error(w, "foto indisponivel", http.StatusNotFound)
		return
	}
	defer arquivo.Close()

	cabeca := make([]byte, 512)
	n, _ := io.ReadFull(arquivo, cabeca)
	if _, err := arquivo.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "foto indisponivel", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(cabeca[:n]))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", modificado, arquivo)
}

// arquivo devolve a foto guardada ainda valida, ou busca de novo. Falha do
// provedor com foto antiga em disco serve a antiga.
func (f *fotosContato) arquivo(ctx context.Context, c store.BuscarConversaComContatoPorIDRow) (string, time.Time, error) {
	foto := filepath.Join(f.diretorio, strconv.FormatInt(c.ID, 10)+".img")
	sem := filepath.Join(f.diretorio, strconv.FormatInt(c.ID, 10)+".sem")

	if caminho, em, ok := f.guardada(foto, sem); ok {
		return caminho, em, nil
	} else if caminho == sem {
		return "", time.Time{}, errSemFoto
	}

	select {
	case f.vagas <- struct{}{}:
		defer func() { <-f.vagas }()
	case <-ctx.Done():
		return "", time.Time{}, ctx.Err()
	}

	// Outra requisicao da mesma conversa pode ter buscado enquanto esta esperava.
	if caminho, em, ok := f.guardada(foto, sem); ok {
		return caminho, em, nil
	} else if caminho == sem {
		return "", time.Time{}, errSemFoto
	}

	err := f.buscar(ctx, c, foto)
	if err == nil {
		os.Remove(sem)
		info, errStat := os.Stat(foto)
		if errStat != nil {
			return "", time.Time{}, errStat
		}
		return foto, info.ModTime(), nil
	}

	if info, errStat := os.Stat(foto); errStat == nil && !errors.Is(err, errSemFoto) {
		slog.Warn("conversas v1: foto do contato, servindo a antiga", "conversa_id", c.ID, "erro", err)
		// A antiga vale mais um pouco: sem isto cada pedido voltava a z-api fora do ar.
		recuo := time.Now().Add(ttlErroFotoContato - ttlFotoContato)
		os.Chtimes(foto, recuo, recuo)
		return foto, info.ModTime(), nil
	}

	f.marcarSemFoto(sem, errors.Is(err, errSemFoto))
	if errors.Is(err, errSemFoto) {
		os.Remove(foto)
	}
	return "", time.Time{}, err
}

// guardada diz se ha resposta valida em disco: a foto (ok) ou a marca de sem
// foto (caminho == sem, ok false).
func (f *fotosContato) guardada(foto, sem string) (string, time.Time, bool) {
	if info, err := os.Stat(sem); err == nil && time.Since(info.ModTime()) < ttlSemFotoContato {
		return sem, time.Time{}, false
	}
	if info, err := os.Stat(foto); err == nil && time.Since(info.ModTime()) < ttlFotoContato {
		return foto, info.ModTime(), true
	}
	return "", time.Time{}, false
}

// marcarSemFoto grava a marca. Erro do provedor dura so ttlErroFotoContato:
// a data da marca e recuada para vencer antes.
func (f *fotosContato) marcarSemFoto(sem string, definitivo bool) {
	if err := os.MkdirAll(f.diretorio, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(sem, nil, 0o644); err != nil {
		return
	}
	if !definitivo {
		recuo := time.Now().Add(ttlErroFotoContato - ttlSemFotoContato)
		os.Chtimes(sem, recuo, recuo)
	}
}

func (f *fotosContato) buscar(ctx context.Context, c store.BuscarConversaComContatoPorIDRow, destino string) error {
	cx, err := f.caixas.PorCodigo(ctx, c.Caixa)
	if err != nil {
		return fmt.Errorf("caixa %s: %w", c.Caixa, err)
	}
	p := f.provedor(cx)
	if p == nil {
		return errSemFoto
	}
	phone := identificadorDaFoto(c)
	if phone == "" {
		return errSemFoto
	}

	chamada, cancel := context.WithTimeout(ctx, timeoutFotoContato)
	defer cancel()

	link, err := p.FotoPerfil(chamada, phone)
	if err != nil {
		return err
	}
	if link == "" {
		return errSemFoto
	}

	req, err := http.NewRequestWithContext(chamada, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return fmt.Errorf("baixar foto: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("baixar foto: status %d", resp.StatusCode)
	}
	corpo, err := io.ReadAll(io.LimitReader(resp.Body, tamanhoMaximoFoto+1))
	if err != nil {
		return fmt.Errorf("baixar foto: %w", err)
	}
	if len(corpo) == 0 || len(corpo) > tamanhoMaximoFoto || !strings.HasPrefix(http.DetectContentType(corpo), "image/") {
		return errSemFoto
	}

	if err := os.MkdirAll(f.diretorio, 0o755); err != nil {
		return err
	}
	temporario := destino + ".tmp"
	if err := os.WriteFile(temporario, corpo, 0o644); err != nil {
		return err
	}
	return os.Rename(temporario, destino)
}

// identificadorDaFoto prefere o telefone: e o que a profile-picture documenta.
// Sem telefone vai o @lid; grupo, o id do grupo.
func identificadorDaFoto(c store.BuscarConversaComContatoPorIDRow) string {
	for _, v := range []*string{c.ContatoGrupoID, c.ContatoTelefone, c.ContatoChatLid} {
		if v != nil && strings.TrimSpace(*v) != "" {
			return strings.TrimPrefix(strings.TrimSpace(*v), "+")
		}
	}
	return ""
}
