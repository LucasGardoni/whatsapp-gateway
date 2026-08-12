// package middleware guarda os http middlewares do gateway.
package middleware

import (
	"crypto/subtle"
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
