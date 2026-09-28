package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
)

// MidiasV1 atende POST /v1/midias (G9): a aplicacao sobe o arquivo e recebe
// o midia_caminho que POST /v1/mensagens exige. Sem isto so quem roda na
// mesma maquina do gateway conseguia mandar anexo.
//
// O arquivo fica em enviadas/<aplicacao>/<AAAA-MM>/<aleatorio>/<nome>: o
// nome original e preservado porque o outbox usa o nome do arquivo como
// fileName do documento no WhatsApp, e o diretorio aleatorio evita que dois
// uploads com o mesmo nome se sobrescrevam.
type MidiasV1 struct {
	midiaDir  string
	maxBytes  int64
	agora     func() time.Time
	aleatorio func() (string, error)
}

func NovoMidiasV1(midiaDir string, maxBytes int64) *MidiasV1 {
	return &MidiasV1{midiaDir: midiaDir, maxBytes: maxBytes, agora: time.Now, aleatorio: sufixoAleatorio}
}

type midiaResponse struct {
	MidiaCaminho string `json:"midia_caminho"`
	Bytes        int64  `json:"bytes"`
}

var caracterForaDoNome = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// nomeSeguro reduz o nome enviado ao basename com caracteres de nome de
// arquivo comuns. Acento e espaco viram "_": o WhatsApp mostra o nome ao
// contato, e um nome que muda entre sistemas de arquivo e pior que um feio.
func nomeSeguro(nome string) string {
	nome = filepath.Base(strings.ReplaceAll(nome, `\`, "/"))
	ext := strings.ToLower(filepath.Ext(nome))
	base := strings.Trim(caracterForaDoNome.ReplaceAllString(strings.TrimSuffix(nome, filepath.Ext(nome)), "_"), "._-")
	if base == "" {
		base = "arquivo"
	}
	if len(base) > 80 {
		base = base[:80]
	}
	return base + ext
}

func (h *MidiasV1) Criar(w http.ResponseWriter, r *http.Request) {
	app, ok := middleware.AplicacaoDoContexto(r.Context())
	if !ok {
		http.Error(w, "aplicacao nao identificada", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBytes+1<<20)
	arquivo, cabecalho, err := r.FormFile("arquivo")
	if err != nil {
		var grande *http.MaxBytesError
		if errors.As(err, &grande) {
			http.Error(w, fmt.Sprintf("arquivo acima de %d bytes", h.maxBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "campo arquivo e obrigatorio (multipart/form-data)", http.StatusBadRequest)
		return
	}
	defer arquivo.Close()

	nome := nomeSeguro(cabecalho.Filename)
	if !midia.ExtensaoAceita(nome) {
		http.Error(w, "extensao nao aceita: "+filepath.Ext(nome), http.StatusBadRequest)
		return
	}

	sufixo, err := h.aleatorio()
	if err != nil {
		slog.Error("midias v1: sufixo aleatorio", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	relativo := filepath.ToSlash(filepath.Join("enviadas", nomeSeguro(app.Codigo), h.agora().Format("2006-01"), sufixo, nome))
	destino, err := midia.ResolverDentroDe(h.midiaDir, relativo)
	if err != nil {
		slog.Error("midias v1: destino fora do diretorio", "caminho", relativo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	bytes, err := gravarArquivo(destino, io.LimitReader(arquivo, h.maxBytes+1))
	if err != nil {
		slog.Error("midias v1: gravar", "caminho", relativo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if bytes > h.maxBytes {
		_ = os.RemoveAll(filepath.Dir(destino))
		http.Error(w, fmt.Sprintf("arquivo acima de %d bytes", h.maxBytes), http.StatusRequestEntityTooLarge)
		return
	}
	if bytes == 0 {
		_ = os.RemoveAll(filepath.Dir(destino))
		http.Error(w, "arquivo vazio", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(midiaResponse{MidiaCaminho: relativo, Bytes: bytes})
}

func gravarArquivo(destino string, origem io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destino), 0o750); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(destino, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, origem)
	if fecha := f.Close(); err == nil {
		err = fecha
	}
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(destino))
	}
	return n, err
}

func sufixoAleatorio() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
