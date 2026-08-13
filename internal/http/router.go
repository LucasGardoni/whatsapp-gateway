// package httpserver monta as rotas do gateway. Nome do pacote diferente
// do diretorio (http/) de proposito -- "http" colidiria com net/http em
// todo arquivo que importar os dois.
package httpserver

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/handler"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
)

// NovoRouter registra as rotas HTTP do gateway. Os tres webhooks da Z-API
// vivem em paths distintos para nao precisar adivinhar um campo
// discriminador no payload -- configure cada um no painel Z-API apontando
// pro path correspondente (secao 4.4 permite endpoint por webhook).
//
// /api/* e chamado so pelo backend do CRM, nunca pelo browser -- protegido
// por tokenServico (GATEWAY_SERVICE_TOKEN, fase 7). /eventos e o
// EventSource do browser, autenticado por token curto na query string
// (ver internal/sse e internal/http/handler/sessoes_sse.go).
func NovoRouter(
	webhookZAPI *handler.WebhookZAPI,
	disparo *handler.Disparo,
	transbordo *handler.Transbordo,
	mensagens *handler.Mensagens,
	sessoesSSE *handler.SessoesSSE,
	eventos *handler.Eventos,
	zapiAdmin *handler.ZAPIAdmin,
	leads *handler.Leads,
	tokenServico string,
	rateLimitPorMinuto int,
) chi.Router {
	r := chi.NewRouter()

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// endpoints publicos (sem token de servico) sao os que ficam expostos
	// pra internet -- so eles levam limite por ip (fase 12). /health (load
	// balancer) e /eventos (autenticado por token curto, conexao longa) nao
	// entram, senao ficariam artificialmente limitados.
	limiteRequisicoes := middleware.NovoLimiteRequisicoes(rateLimitPorMinuto, time.Minute)
	r.Group(func(r chi.Router) {
		r.Use(limiteRequisicoes.Middleware)

		r.Post("/webhooks/zapi/mensagens", webhookZAPI.OnMessageReceived)
		r.Post("/webhooks/zapi/status-mensagem", webhookZAPI.OnMessageStatus)
		r.Post("/webhooks/zapi/desconexao", webhookZAPI.OnWhatsappDisconnected)

		// webhook generico de ingestao de leads (fase 11) -- GET e o
		// handshake de verificacao que a Meta exige antes de aceitar
		// mandar POST aqui.
		r.Get("/webhooks/leads/{origem}", leads.VerificarWebhook)
		r.Post("/webhooks/leads/{origem}", leads.Webhook)

		r.Post("/disparos", disparo.Criar)
		r.Get("/c/{token}", transbordo.RedirecionarClique)
	})

	r.Get("/eventos", eventos.Servir)

	r.Group(func(r chi.Router) {
		r.Use(middleware.ExigirTokenServico(tokenServico))
		r.Post("/api/mensagens", mensagens.Criar)
		r.Post("/api/sessoes-sse", sessoesSSE.Criar)

		// gestao de fila e qr code de reconexao (fase 9) -- painel de
		// supervisao do CRM, nunca exposto ao browser diretamente.
		r.Get("/api/zapi/fila", zapiAdmin.Fila)
		r.Delete("/api/zapi/fila", zapiAdmin.LimparFila)
		r.Delete("/api/zapi/fila/{id}", zapiAdmin.LimparItemFila)
		r.Get("/api/zapi/qrcode", zapiAdmin.QRCode)

		// job de reenvio e upload de csv (fase 11) -- dono e o supervisor,
		// a tela que aciona e 100% CRM (ver plano, secao "Fase 11").
		r.Post("/api/leads/reenvio", disparo.Reenviar)
		r.Post("/api/leads/importar-csv", leads.ImportarCSV)
	})

	return r
}
