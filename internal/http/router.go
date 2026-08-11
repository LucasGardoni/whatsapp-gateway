// package httpserver monta as rotas do gateway. Nome do pacote diferente
// do diretorio (http/) de proposito -- "http" colidiria com net/http em
// todo arquivo que importar os dois.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/handler"
)

// NovoRouter registra as rotas HTTP do gateway. Os tres webhooks da Z-API
// vivem em paths distintos para nao precisar adivinhar um campo
// discriminador no payload -- configure cada um no painel Z-API apontando
// pro path correspondente (secao 4.4 permite endpoint por webhook).
func NovoRouter(webhookZAPI *handler.WebhookZAPI, disparo *handler.Disparo, transbordo *handler.Transbordo) chi.Router {
	r := chi.NewRouter()

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Post("/webhooks/zapi/mensagens", webhookZAPI.OnMessageReceived)
	r.Post("/webhooks/zapi/status-mensagem", webhookZAPI.OnMessageStatus)
	r.Post("/webhooks/zapi/desconexao", webhookZAPI.OnWhatsappDisconnected)

	r.Post("/disparos", disparo.Criar)
	r.Get("/c/{token}", transbordo.RedirecionarClique)

	return r
}
