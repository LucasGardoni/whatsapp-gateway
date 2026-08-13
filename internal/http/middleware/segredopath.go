package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// SegredoPathParam e o nome do parametro de rota que carrega o segredo.
// Vive aqui, e nao no router, pra que middleware e rota nao possam
// discordar do nome sem quebrar a compilacao.
const SegredoPathParam = "segredo"

// ExigirSegredoPath protege os webhooks de entrada (Z-API e ingestao de
// leads). Esses endpoints sao chamados por um terceiro que nao consegue
// mandar header de autenticacao -- o painel da Z-API so aceita uma URL --,
// entao o unico lugar onde cabe um segredo compartilhado e o proprio path
// (P1-10, fase 2 da auditoria de integracao).
//
// Sem isto, qualquer um que descubra a URL publica do gateway injeta
// mensagem e lead direto na base: os webhooks nao tem nenhuma outra
// checagem de origem.
//
// Responde 404, nao 401, de proposito: pra quem errou o segredo, o
// endpoint nao deve nem parecer existir. segredoEsperado vazio significa
// que o operador nao configurou WEBHOOK_PATH_SECRET, e a rota fica fechada
// (fail closed, mesmo padrao de ExigirTokenServico).
func ExigirSegredoPath(segredoEsperado string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if segredoEsperado == "" {
				http.NotFound(w, r)
				return
			}

			recebido := chi.URLParam(r, SegredoPathParam)
			if subtle.ConstantTimeCompare([]byte(recebido), []byte(segredoEsperado)) != 1 {
				http.NotFound(w, r)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
