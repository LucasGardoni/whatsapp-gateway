package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
)

// ResolvedorCaixa e o pedaco de caixa.Registro que o webhook usa.
type ResolvedorCaixa interface {
	PorSegredo(ctx context.Context, segredo string) (caixa.Caixa, bool, error)
}

// CaixaPeloSegredo substitui ExigirSegredoPath nos webhooks da z-api (G1):
// o segredo do path deixa de ser um so e passa a dizer DE QUAL numero veio o
// callback. Segredo desconhecido responde 404, como antes -- para quem
// errou, o endpoint nao existe. Falha ao ler as caixas responde 500, para a
// z-api reenviar em vez de a mensagem se perder.
func CaixaPeloSegredo(caixas ResolvedorCaixa) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok, err := caixas.PorSegredo(r.Context(), chi.URLParam(r, SegredoPathParam))
			if err != nil {
				slog.Error("webhook: resolver caixa pelo segredo", "erro", err)
				http.Error(w, "erro interno", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(caixa.NoContexto(r.Context(), c)))
		})
	}
}
