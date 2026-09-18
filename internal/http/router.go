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
//
// Os webhooks levam segredoWebhook no path (WEBHOOK_PATH_SECRET) porque
// quem os chama e um terceiro que nao manda header -- ver
// middleware.ExigirSegredoPath. Sem esse segredo o gateway nao pode ser
// exposto na internet: os webhooks sao gravacao de dados sem autenticacao.
func NovoRouter(
	webhookZAPI *handler.WebhookZAPI,
	disparo *handler.Disparo,
	transbordo *handler.Transbordo,
	mensagens *handler.Mensagens,
	sessoesSSE *handler.SessoesSSE,
	eventos *handler.Eventos,
	zapiAdmin *handler.ZAPIAdmin,
	leads *handler.Leads,
	canais *handler.Canais,
	tokenServico string,
	// autenticadorApp e a identidade por aplicacao (barramento, fase 1).
	// Pode ser nil: nesse caso /api/* cai no token de servico unico, que e
	// exatamente o comportamento anterior.
	autenticadorApp *middleware.AutenticadorAplicacao,
	segredoWebhook string,
	rateLimitPorMinuto int,
) chi.Router {
	r := chi.NewRouter()

	// correlacao antes de tudo: vale para toda rota, inclusive as que
	// respondem erro, que sao justamente as que se quer investigar depois.
	r.Use(middleware.RequestID)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// endpoints publicos (sem token de servico) sao os que ficam expostos
	// pra internet -- so eles levam limite por ip (fase 12). /health (load
	// balancer) e /eventos (autenticado por token curto, conexao longa) nao
	// entram, senao ficariam artificialmente limitados.
	limiteRequisicoes := middleware.NovoLimiteRequisicoes(rateLimitPorMinuto, time.Minute)
	segredo := "/{" + middleware.SegredoPathParam + "}"

	r.Group(func(r chi.Router) {
		r.Use(limiteRequisicoes.Middleware)
		r.Use(middleware.ExigirSegredoPath(segredoWebhook))

		r.Post("/webhooks/zapi"+segredo+"/mensagens", webhookZAPI.OnMessageReceived)
		r.Post("/webhooks/zapi"+segredo+"/status-mensagem", webhookZAPI.OnMessageStatus)
		r.Post("/webhooks/zapi"+segredo+"/desconexao", webhookZAPI.OnWhatsappDisconnected)

		// resultado assincrono do envio (P1-09). Configurar no campo
		// "Ao enviar" do painel Z-API -- ate a fase 6 essa rota nao
		// existia e o campo tinha de ficar vazio.
		r.Post("/webhooks/zapi"+segredo+"/envio", webhookZAPI.OnMessageSend)

		// webhook generico de ingestao de leads (fase 11) -- GET e o
		// handshake de verificacao que a Meta exige antes de aceitar
		// mandar POST aqui. O segredo vem antes de {origem} pra que uma
		// origem nova nao possa ser adicionada sem ele.
		r.Get("/webhooks/leads"+segredo+"/{origem}", leads.VerificarWebhook)
		r.Post("/webhooks/leads"+segredo+"/{origem}", leads.Webhook)
	})

	// /c/{token} e o unico endpoint que um cliente final abre no browser: o
	// link do transbordo. Nao pode levar segredo no path (o link vai pro
	// cliente) nem token de servico -- o proprio token do transbordo e a
	// autenticacao, e e de uso unico.
	r.Group(func(r chi.Router) {
		r.Use(limiteRequisicoes.Middleware)
		r.Get("/c/{token}", transbordo.RedirecionarClique)
	})

	r.Get("/eventos", eventos.Servir)

	r.Group(func(r chi.Router) {
		// aceita o token unico e o token por aplicacao em paralelo (fase 1
		// do barramento) -- o unico sai na fase 8.
		r.Use(middleware.ExigirServicoOuAplicacao(tokenServico, autenticadorApp))
		r.Post("/api/mensagens", mensagens.Criar)
		r.Post("/api/sessoes-sse", sessoesSSE.CriarLegado)

		// /disparos criava token de transbordo e resolvia @lid sem nenhuma
		// autenticacao (P1-14) -- exposto na internet, um estranho gerava
		// disparo em nome da empresa. Quem chama e o backend do CRM, entao
		// o token de servico e o mesmo de /api/*. O path segue sem /api/
		// por compatibilidade com o que a auditoria documentou.
		r.Post("/disparos", disparo.Criar)

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

	// /v1/* e o barramento (fases 3 e 4). Diferente de /api/*, aqui a
	// autenticacao e ESTRITA: so token de aplicacao, nunca o
	// GATEWAY_SERVICE_TOKEN unico. Sem aplicacao identificada nao ha como
	// escopar canal nem compor a chave do hub, e um escopo adivinhado
	// atravessaria a fronteira entre consumidores -- que e a unica coisa
	// que este prefixo existe para garantir.
	//
	// autenticadorApp nil (sem banco na subida) deixa /v1/* fora do ar em
	// vez de aberto.
	if autenticadorApp != nil {
		r.Group(func(r chi.Router) {
			r.Use(autenticadorApp.Middleware)

			r.Post("/v1/sessoes", sessoesSSE.Criar)

			// canal e lista de entrega (fase 4) -- todas idempotentes, para
			// a aplicacao reconciliar o estado dela sem saber o que ja
			// mandou antes.
			r.Put("/v1/canais/{canal_externo}", canais.PutCanal)
			r.Put("/v1/canais/{canal_externo}/assinantes/{destino}", canais.PutAssinante)
			r.Delete("/v1/canais/{canal_externo}/assinantes/{destino}", canais.DeleteAssinante)
			r.Get("/v1/canais/{canal_externo}/assinantes", canais.GetAssinantes)
		})
	}

	return r
}
