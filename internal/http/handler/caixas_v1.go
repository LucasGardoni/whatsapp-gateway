package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
)

// CaixasV1 atende GET /v1/caixas (G1): os numeros ativos, para a aplicacao
// ligar o cadastro dela a caixa do gateway. Nenhuma credencial sai daqui.
type CaixasV1 struct {
	caixas listaCaixas
}

type listaCaixas interface {
	Ativas(ctx context.Context) ([]caixa.Caixa, error)
}

func NovoCaixasV1(caixas listaCaixas) *CaixasV1 {
	return &CaixasV1{caixas: caixas}
}

type caixaResponse struct {
	ID       int64  `json:"id"`
	Codigo   string `json:"codigo"`
	Nome     string `json:"nome"`
	Provedor string `json:"provedor"`
}

type listarCaixasResponse struct {
	Caixas []caixaResponse `json:"caixas"`
	Padrao string          `json:"padrao"`
}

type caixaPadrao interface {
	Padrao() string
}

func (h *CaixasV1) Listar(w http.ResponseWriter, r *http.Request) {
	ativas, err := h.caixas.Ativas(r.Context())
	if err != nil {
		slog.Error("caixas v1: listar", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	resp := listarCaixasResponse{Caixas: make([]caixaResponse, 0, len(ativas))}
	if p, ok := h.caixas.(caixaPadrao); ok {
		resp.Padrao = p.Padrao()
	}
	for _, c := range ativas {
		resp.Caixas = append(resp.Caixas, caixaResponse{ID: c.ID, Codigo: c.Codigo, Nome: c.Nome, Provedor: c.Provedor})
	}
	responderJSON(w, resp)
}
