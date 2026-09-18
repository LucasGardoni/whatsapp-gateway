// package middleware guarda os http middlewares do gateway.
package middleware

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// ExigirTokenServico protege endpoints chamados so pelo backend do CRM,
// nunca pelo browser (POST /api/mensagens, POST /api/sessoes-sse -- fase
// 7). tokenEsperado vem de GATEWAY_SERVICE_TOKEN; vazio significa que o
// operador nao configurou a integracao com o CRM ainda, e o endpoint fica
// fechado (fail closed) em vez de aceitar qualquer coisa.
func ExigirTokenServico(tokenEsperado string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokenEsperado == "" {
				http.Error(w, "integracao com o crm nao configurada", http.StatusServiceUnavailable)
				return
			}

			recebido := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(recebido), []byte(tokenEsperado)) != 1 {
				http.Error(w, "nao autorizado", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ExigirServicoOuAplicacao e a ponte da fase 1 do barramento: aceita tanto
// o GATEWAY_SERVICE_TOKEN unico quanto um token por aplicacao, para as
// rotas /api/* nao quebrarem enquanto o CRM ainda nao migrou para /v1/
// (o token legado sai na fase 8).
//
// A ordem importa. O autenticador vem primeiro porque na subida o token
// legado e sincronizado como token da aplicacao 'crm' -- entao o caminho
// normal ja identifica a aplicacao e grava procedencia. O token legado so
// e conferido depois, como rede de seguranca para quando essa
// sincronizacao nao aconteceu (banco indisponivel na subida), e nesse
// caso a requisicao passa SEM aplicacao no contexto: procedencia ausente
// e melhor que procedencia adivinhada.
func ExigirServicoOuAplicacao(tokenEsperado string, autenticador *AutenticadorAplicacao) func(http.Handler) http.Handler {
	legado := ExigirTokenServico(tokenEsperado)

	return func(next http.Handler) http.Handler {
		fallback := legado(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if token == "" || autenticador == nil {
				fallback.ServeHTTP(w, r)
				return
			}

			app, err := autenticador.Resolver(r.Context(), token)
			if err != nil {
				slog.Error("aplicacao: resolver token", "erro", err)
				http.Error(w, "erro interno", http.StatusInternalServerError)
				return
			}
			if app == nil {
				fallback.ServeHTTP(w, r)
				return
			}

			next.ServeHTTP(w, r.WithContext(ComAplicacao(r.Context(), *app)))
		})
	}
}
