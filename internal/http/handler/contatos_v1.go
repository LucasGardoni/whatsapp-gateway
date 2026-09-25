package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
)

// ContatosV1 atende G5 (docs/PLANO_MULTICAIXA_E_CONVERSAS.md): GET
// /v1/contatos, espelho paginado da agenda da instancia. Existe para a
// sincronizacao agendada das aplicacoes, nao para busca a cada digitacao --
// por isso o cache curto por pagina, que segura varias aplicacoes pedindo a
// mesma pagina sem estourar o limite da z-api.
type ContatosV1 struct {
	caixas caixasV1
	// fonte devolve a agenda DA CAIXA (G1). Nil = caixa sem agenda no
	// provedor (fake): responde lista vazia, que e o fim da sincronizacao.
	fonte func(caixa.Caixa) fonteContatos
	ttl   time.Duration

	mu    sync.Mutex
	cache map[string]paginaContatosCache
}

type fonteContatos interface {
	Contatos(ctx context.Context, page, pageSize int) ([]zapi.Contato, error)
}

type paginaContatosCache struct {
	contatos []zapi.Contato
	expiraEm time.Time
}

const (
	pageSizePadraoContatos = 100
	pageSizeMaximoContatos = 500
	ttlCacheContatos       = 60 * time.Second
)

func NovoContatosV1(caixas *caixa.Registro) *ContatosV1 {
	return novoContatosV1(caixas, func(c caixa.Caixa) fonteContatos {
		if cl, ok := caixas.ZAPI(c); ok {
			return cl
		}
		return nil
	})
}

func novoContatosV1(caixas caixasV1, fonte func(caixa.Caixa) fonteContatos) *ContatosV1 {
	return &ContatosV1{caixas: caixas, fonte: fonte, ttl: ttlCacheContatos, cache: map[string]paginaContatosCache{}}
}

type contatoAgendaResponse struct {
	Telefone      string  `json:"telefone"`
	TelefoneE164  *string `json:"telefone_e164"`
	Nome          *string `json:"nome"`
	NomeCurto     *string `json:"nome_curto"`
	NomeComercial *string `json:"nome_comercial"`
	NomePerfil    *string `json:"nome_perfil"`
}

type listarContatosResponse struct {
	Caixa    string                  `json:"caixa"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
	Contatos []contatoAgendaResponse `json:"contatos"`
}

// Listar atende GET /v1/contatos?caixa=&page=&page_size=. Pagina alem da
// ultima vem com a lista vazia -- e o sinal de fim para quem sincroniza.
// telefone_e164 vem nulo quando o numero nao e brasileiro valido (grupo,
// numero estrangeiro): o bruto segue em `telefone`.
func (h *ContatosV1) Listar(w http.ResponseWriter, r *http.Request) {
	cx, ok := caixaDaRequisicao(w, r, h.caixas, r.URL.Query().Get("caixa"))
	if !ok {
		return
	}

	page, ok := inteiroDaQuery(w, r, "page", 1)
	if !ok {
		return
	}
	if page < 1 {
		http.Error(w, "page comeca em 1", http.StatusBadRequest)
		return
	}
	pageSize, ok := inteiroDaQuery(w, r, "page_size", pageSizePadraoContatos)
	if !ok {
		return
	}
	if pageSize < 1 || pageSize > pageSizeMaximoContatos {
		pageSize = pageSizeMaximoContatos
	}

	contatos, err := h.pagina(r.Context(), cx, int(page), int(pageSize))
	if err != nil {
		slog.Error("contatos v1: listar", "caixa", cx.Codigo, "page", page, "erro", err)
		http.Error(w, "erro ao consultar contatos no provedor", http.StatusBadGateway)
		return
	}

	resp := listarContatosResponse{
		Caixa:    cx.Codigo,
		Page:     int(page),
		PageSize: int(pageSize),
		Contatos: make([]contatoAgendaResponse, 0, len(contatos)),
	}
	for _, c := range contatos {
		item := contatoAgendaResponse{
			Telefone:      c.Phone,
			Nome:          naoVazio(strings.TrimSpace(c.Name)),
			NomeCurto:     naoVazio(strings.TrimSpace(c.Short)),
			NomeComercial: naoVazio(strings.TrimSpace(c.Vname)),
			NomePerfil:    naoVazio(strings.TrimSpace(c.Notify)),
		}
		if e164, err := identidade.NormalizarE164(c.Phone); err == nil {
			item.TelefoneE164 = &e164
		}
		resp.Contatos = append(resp.Contatos, item)
	}

	responderJSON(w, resp)
}

func (h *ContatosV1) pagina(ctx context.Context, cx caixa.Caixa, page, pageSize int) ([]zapi.Contato, error) {
	fonte := h.fonte(cx)
	if fonte == nil {
		return nil, nil
	}
	chave := fmt.Sprintf("%d:%d:%d", cx.ID, page, pageSize)
	agora := time.Now()

	h.mu.Lock()
	if c, ok := h.cache[chave]; ok && agora.Before(c.expiraEm) {
		h.mu.Unlock()
		return c.contatos, nil
	}
	h.mu.Unlock()

	contatos, err := fonte.Contatos(ctx, page, pageSize)
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	for k, c := range h.cache {
		if agora.After(c.expiraEm) {
			delete(h.cache, k)
		}
	}
	h.cache[chave] = paginaContatosCache{contatos: contatos, expiraEm: agora.Add(h.ttl)}
	h.mu.Unlock()
	return contatos, nil
}
