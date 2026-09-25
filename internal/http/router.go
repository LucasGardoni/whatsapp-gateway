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
	"github.com/LucasGardoni/whatsapp-gateway/internal/metrica"
)

// NovoRouter registra as rotas HTTP do gateway. Os tres webhooks da Z-API
// vivem em paths distintos para nao precisar adivinhar um campo
// discriminador no payload -- configure cada um no painel Z-API apontando
// pro path correspondente (secao 4.4 permite endpoint por webhook).
//
// /api/* e chamado so por backend de aplicacao, nunca pelo browser --
// protegido por token de aplicacao (barramento, fase 1). /eventos e o
// EventSource do browser, autenticado por token curto na query string
// (ver internal/sse e internal/http/handler/sessoes_sse.go).
//
// Fase 8: o GATEWAY_SERVICE_TOKEN unico acabou. Toda rota autenticada
// resolve uma aplicacao, entao toda escrita tem procedencia -- nao ha
// mais o caminho anonimo que gravava sem saber quem chamou.
//
// Os webhooks levam segredoWebhook no path (WEBHOOK_PATH_SECRET) porque
// quem os chama e um terceiro que nao manda header -- ver
// middleware.ExigirSegredoPath. Sem esse segredo o gateway nao pode ser
// exposto na internet: os webhooks sao gravacao de dados sem autenticacao.
func NovoRouter(
	webhookZAPI *handler.WebhookZAPI,
	disparo *handler.Disparo,
	transbordo *handler.Transbordo,
	mensagensV1 *handler.MensagensV1,
	sessoesSSE *handler.SessoesSSE,
	eventos *handler.Eventos,
	zapiAdmin *handler.ZAPIAdmin,
	leads *handler.Leads,
	canais *handler.Canais,
	conversas *handler.ConversasV1,
	contatos *handler.ContatosV1,
	caixasV1 *handler.CaixasV1,
	metricas *handler.Metricas,
	// caixas resolve o segredo do path dos webhooks da z-api em caixa (G1):
	// cada numero tem o seu, e e ele que diz de onde veio o callback.
	caixas middleware.ResolvedorCaixa,
	// autenticadorApp e a identidade por aplicacao (barramento, fase 1).
	// Desde a fase 8 ele e a UNICA autenticacao de servico que existe: o
	// GATEWAY_SERVICE_TOKEN unico foi removido. Nil deixa as rotas
	// autenticadas fora do ar, em vez de abertas.
	autenticadorApp *middleware.AutenticadorAplicacao,
	// registro alimenta o middleware de metricas (fase 9). Nil desliga a
	// contagem sem desligar rota nenhuma -- metrica ausente e perda de
	// visibilidade, nao de seguranca, e nao ha por que fechar o
	// barramento por causa dela.
	registro *metrica.Registro,
	segredoWebhook string,
	rateLimitPorMinuto int,
	rateLimitAplicacaoPorMinuto int,
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

	// as rotas AUTENTICADAS levam limite por aplicacao (fase 9), nao por
	// IP: atras de um proxy reverso todas as aplicacoes chegam com o mesmo
	// IP, entao um limite por IP ali ou e alto o bastante para nao
	// proteger ninguem, ou uma aplicacao em laco consome o teto de todas.
	limiteAplicacao := middleware.NovoLimitePorAplicacao(rateLimitAplicacaoPorMinuto, time.Minute)
	// as duas travessias que toda rota autenticada faz, na ordem que
	// importa: metrica POR FORA do limite, para que o 429 apareca em
	// gateway_erros_* -- uma aplicacao sendo barrada e precisamente o que
	// se quer ver no painel, e medir por dentro esconderia isso.
	porAplicacao := func(r chi.Router) {
		r.Use(autenticadorApp.Middleware)
		if registro != nil {
			r.Use(middleware.Metricas(registro))
		}
		r.Use(limiteAplicacao.Middleware)
	}

	segredo := "/{" + middleware.SegredoPathParam + "}"

	// webhooks da z-api: o segredo e o da caixa (caixa.webhook_segredo). A
	// caixa semente recebe o WEBHOOK_PATH_SECRET na subida, entao a URL ja
	// configurada no painel continua valendo.
	r.Group(func(r chi.Router) {
		r.Use(limiteRequisicoes.Middleware)
		r.Use(middleware.CaixaPeloSegredo(caixas))

		r.Post("/webhooks/zapi"+segredo+"/mensagens", webhookZAPI.OnMessageReceived)
		r.Post("/webhooks/zapi"+segredo+"/status-mensagem", webhookZAPI.OnMessageStatus)
		r.Post("/webhooks/zapi"+segredo+"/desconexao", webhookZAPI.OnWhatsappDisconnected)

		// resultado assincrono do envio (P1-09). Configurar no campo
		// "Ao enviar" do painel Z-API -- ate a fase 6 essa rota nao
		// existia e o campo tinha de ficar vazio.
		r.Post("/webhooks/zapi"+segredo+"/envio", webhookZAPI.OnMessageSend)
	})

	r.Group(func(r chi.Router) {
		r.Use(limiteRequisicoes.Middleware)
		r.Use(middleware.ExigirSegredoPath(segredoWebhook))

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

	// as rotas de servico que sobraram da fase 8. Elas nao viraram /v1/
	// porque nao sao barramento: sao operacoes do CRM sobre o dominio do
	// WhatsApp (fila da Z-API, disparo, importacao de lead) que continuam
	// exatamente como estavam -- so a autenticacao mudou, de token unico
	// para token de aplicacao.
	//
	// Envio de mensagem e sessao de SSE sairam daqui: viraram
	// POST /v1/mensagens e POST /v1/sessoes.
	if autenticadorApp != nil {
		r.Group(func(r chi.Router) {
			porAplicacao(r)

			// /disparos criava token de transbordo e resolvia @lid sem
			// nenhuma autenticacao (P1-14) -- exposto na internet, um
			// estranho gerava disparo em nome da empresa. O path segue sem
			// /api/ por compatibilidade com o que a auditoria documentou.
			r.Post("/disparos", disparo.Criar)

			// gestao de fila e qr code de reconexao (fase 9) -- painel de
			// supervisao do CRM, nunca exposto ao browser diretamente.
			r.Get("/api/zapi/fila", zapiAdmin.Fila)
			r.Delete("/api/zapi/fila", zapiAdmin.LimparFila)
			r.Delete("/api/zapi/fila/{id}", zapiAdmin.LimparItemFila)
			r.Get("/api/zapi/qrcode", zapiAdmin.QRCode)

			// token efemero da sdk de chamadas (@z-api/call). O browser nunca
			// chama aqui: a aplicacao pede, confere o usuario e repassa.
			r.Post("/api/zapi/chamadas/token", zapiAdmin.TokenChamada)

			// job de reenvio e upload de csv (fase 11) -- dono e o
			// supervisor, a tela que aciona e 100% CRM (ver plano, secao
			// "Fase 11").
			r.Post("/api/leads/reenvio", disparo.Reenviar)
			r.Post("/api/leads/importar-csv", leads.ImportarCSV)

			// metricas por aplicacao (fase 9). Nao e /v1/ porque nao e
			// barramento: e observabilidade do gateway, e quem a le nao
			// manda nem recebe mensagem por ela.
			//
			// Fica atras do token de aplicacao E de
			// aplicacao.pode_ler_metricas, porque a resposta mostra o
			// trafego de TODAS as aplicacoes -- ter token nao basta, seria
			// uma aplicacao vendo o movimento das outras. /metrics aberto
			// e o default de quase todo servico, e aqui seria a fronteira
			// da secao 2 furada pela porta dos fundos.
			if metricas != nil {
				r.Get("/metrics", metricas.Servir)
			}
		})
	}

	// /v1/* e o barramento (fases 3 e 4). Sem aplicacao identificada nao
	// ha como escopar canal nem compor a chave do hub, e um escopo
	// adivinhado atravessaria a fronteira entre consumidores -- que e a
	// unica coisa que este prefixo existe para garantir.
	//
	// autenticadorApp nil (sem banco na subida) deixa /v1/* fora do ar em
	// vez de aberto.
	if autenticadorApp != nil {
		r.Group(func(r chi.Router) {
			porAplicacao(r)

			r.Post("/v1/sessoes", sessoesSSE.Criar)

			// entrada unificada do barramento (fase 6): os dois canais no
			// mesmo endpoint, escolhidos pelo campo `canal` do corpo. Era
			// POST /api/mensagens em paralelo ate a fase 8, que removeu a
			// rota legada -- esta sempre foi a mesma implementacao.
			r.Post("/v1/mensagens", mensagensV1.Criar)

			// canal e lista de entrega (fase 4) -- todas idempotentes, para
			// a aplicacao reconciliar o estado dela sem saber o que ja
			// mandou antes.
			r.Put("/v1/canais/{canal_externo}", canais.PutCanal)
			r.Put("/v1/canais/{canal_externo}/assinantes/{destino}", canais.PutAssinante)
			r.Delete("/v1/canais/{canal_externo}/assinantes/{destino}", canais.DeleteAssinante)
			r.Get("/v1/canais/{canal_externo}/assinantes", canais.GetAssinantes)

			// historico do canal (secao 6.3) -- devolve o ciphertext; quem
			// decide quem pode ler e a aplicacao, antes de chamar.
			r.Get("/v1/canais/{canal_externo}/mensagens", mensagensV1.Historico)

			// G2/G3 (docs/PLANO_MULTICAIXA_E_CONVERSAS.md): leitura de
			// conversa e mensagem de WhatsApp que o gateway mesmo gravou.
			// Sem elas o CRM que consome o barramento nao tinha como saber
			// o conversa_id que POST /v1/mensagens exige.
			r.Get("/v1/conversas", conversas.Listar)
			r.Get("/v1/conversas/{id}/mensagens", conversas.Mensagens)
			// G6: leitura por aplicacao, alimenta nao_lidas de GET /v1/conversas.
			r.Post("/v1/conversas/{id}/lida", conversas.MarcarLida)
			// G4: conversa com numero que nunca falou com a caixa.
			r.Post("/v1/conversas", conversas.Abrir)
			// G5: agenda da instancia, para sincronizacao agendada.
			r.Get("/v1/contatos", contatos.Listar)
			// G1: numeros ativos, para a aplicacao ligar o cadastro dela.
			r.Get("/v1/caixas", caixasV1.Listar)
		})
	}

	return r
}
