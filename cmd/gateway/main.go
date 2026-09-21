package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/alerta"
	"github.com/LucasGardoni/whatsapp-gateway/internal/chatinterno"
	"github.com/LucasGardoni/whatsapp-gateway/internal/config"
	"github.com/LucasGardoni/whatsapp-gateway/internal/dlp"
	httpserver "github.com/LucasGardoni/whatsapp-gateway/internal/http"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/handler"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/ingestao"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/outbox"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
	"github.com/LucasGardoni/whatsapp-gateway/internal/retencao"
	"github.com/LucasGardoni/whatsapp-gateway/internal/saude"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// aplicacaoLegada e o codigo da aplicacao que herda o GATEWAY_SERVICE_TOKEN
// -- todo o historico anterior ao barramento veio dela (ver backfill da
// migration 00016).
const aplicacaoLegada = "crm"

func main() {
	if err := run(); err != nil {
		slog.Error("gateway encerrado com erro", "erro", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("carregar config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("criar pool postgres: %w", err)
	}
	defer pool.Close()

	queries := store.New(pool)
	zapiCliente := zapi.NovoCliente(cfg.ZAPIInstanceID, cfg.ZAPIInstanceToken, cfg.ZAPIClientToken)
	motorDLP := dlp.NovoMotor(dlp.Config{
		DominiosPermitidos: cfg.DLPDominiosPermitidos,
		SomenteAvisar:      cfg.DLPSomenteAvisar,
	})

	// hub e assinador sao o lado go do tempo real. O assinador substituiu o
	// TokenStore em memoria na fase 3 do barramento: token assinado por
	// HMAC nao precisa de estado compartilhado, entao deixa de haver o
	// teto de instancia unica.
	hub := sse.NovoHub()
	assinadorSSE := sse.NovoAssinadorSessao(cfg.SSESigningKey)

	worker := outbox.NovoWorker(queries, zapiCliente, motorDLP, outbox.Config{MidiaDir: cfg.MidiaDir})
	worker.Hub = hub

	monitorSaude := saude.NovoMonitor(zapiCliente, queries, saude.Config{NomeProvedor: "zapi"})

	pollerChatInterno := chatinterno.NovoPoller(queries, hub, chatinterno.Config{})

	monitorAlerta := alerta.NovoMonitor(queries, alerta.Config{})

	monitorRetencao := retencao.NovoMonitor(queries, retencao.Config{})

	baixador := midia.NovoBaixador(cfg.MidiaDir)
	webhookZAPI := handler.NovoWebhookZAPI(pool, baixador)
	webhookZAPI.Hub = hub

	identidadeCliente := identidade.NovoCliente(cfg.ZAPIInstanceID, cfg.ZAPIInstanceToken, cfg.ZAPIClientToken)
	disparo := handler.NovoDisparo(pool, identidadeCliente, cfg.PublicBaseURL)
	transbordo := handler.NovoTransbordo(pool)
	mensagens := handler.NovoMensagens(pool, cfg.MidiaDir)
	mensagens.Hub = hub
	mensagensV1 := handler.NovoMensagensV1(pool, cfg.MidiaDir)
	mensagensV1.Hub = hub
	sessoesSSE := handler.NovoSessoesSSE(assinadorSSE)
	eventos := handler.NovoEventos(hub, assinadorSSE, cfg.CORSOrigemCRM)
	zapiAdmin := handler.NovoZAPIAdmin(zapiCliente)
	canais := handler.NovoCanais(pool)
	leads := handler.NovoLeads(pool, ingestao.RegistroPadrao())
	leads.VerifyToken = cfg.MetaWebhookVerifyToken

	if cfg.WebhookPathSecret == "" {
		slog.Warn("WEBHOOK_PATH_SECRET vazio: os webhooks de entrada respondem 404 e nada entra no gateway. Nao exponha o gateway na internet sem ele")
	}

	if assinadorSSE == nil {
		slog.Warn("SSE_SIGNING_KEY vazia: o tempo real fica desligado (POST /v1/sessoes, POST /api/sessoes-sse e GET /eventos respondem 503). Gere uma com: openssl rand -base64 32")
	}

	// Identidade por aplicacao (barramento, fase 1). A migration 00016 cria
	// a linha 'crm' com um token_hash sentinela porque migration nao le
	// ambiente -- e aqui que ela herda o GATEWAY_SERVICE_TOKEN atual, para
	// o CRM continuar autenticando sem nenhuma troca de credencial.
	autenticadorApp := middleware.NovoAutenticadorAplicacao(queries)
	if cfg.GatewayServiceToken != "" {
		if _, err := queries.SincronizarTokenAplicacao(ctx, store.SincronizarTokenAplicacaoParams{
			Codigo:    aplicacaoLegada,
			Nome:      "CRM de corretores",
			TokenHash: middleware.HashToken(cfg.GatewayServiceToken),
		}); err != nil {
			// nao e fatal: o token de servico unico segue valendo em
			// paralelo ate a fase 8, entao o gateway ainda atende -- so
			// sem gravar procedencia.
			slog.Error("nao foi possivel sincronizar o token da aplicacao crm; /api/* segue no token de servico unico, sem procedencia", "erro", err)
		}
	}

	router := httpserver.NovoRouter(webhookZAPI, disparo, transbordo, mensagens, mensagensV1, sessoesSSE, eventos, zapiAdmin, leads, canais, cfg.GatewayServiceToken, autenticadorApp, cfg.WebhookPathSecret, cfg.RateLimitPorMinuto)

	// Sem timeout nenhum, uma conexao aberta e ociosa segura um goroutine e
	// um descritor para sempre -- e o gateway fica exposto na internet
	// (webhooks), onde isso e trivial de provocar.
	//
	// WriteTimeout fica ZERADO de proposito: ele vale para a resposta
	// inteira, e /eventos e um stream que dura horas. Qualquer valor aqui
	// derrubaria o SSE do corretor no meio do expediente. Quem cobre o
	// caso patologico e o IdleTimeout (conexao sem requisicao) somado ao
	// ReadHeaderTimeout (cliente que abre e nao fala).
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("servidor iniciado", "porta", cfg.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	workerErr := make(chan error, 1)
	go func() {
		slog.Info("outbox worker iniciado")
		workerErr <- worker.Executar(ctx)
	}()

	saudeErr := make(chan error, 1)
	go func() {
		slog.Info("monitor de saude iniciado")
		saudeErr <- monitorSaude.Executar(ctx)
	}()

	chatInternoErr := make(chan error, 1)
	go func() {
		slog.Info("poller de chat interno iniciado")
		chatInternoErr <- pollerChatInterno.Executar(ctx)
	}()

	alertaErr := make(chan error, 1)
	go func() {
		slog.Info("monitor de alerta de volume iniciado")
		alertaErr <- monitorAlerta.Executar(ctx)
	}()

	retencaoErr := make(chan error, 1)
	go func() {
		slog.Info("monitor de retencao iniciado")
		retencaoErr <- monitorRetencao.Executar(ctx)
	}()

	select {
	case <-ctx.Done():
		slog.Info("sinal de encerramento recebido, iniciando shutdown")
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("servidor http: %w", err)
		}
		return nil
	case err := <-workerErr:
		if err != nil {
			return fmt.Errorf("outbox worker: %w", err)
		}
		return nil
	case err := <-saudeErr:
		if err != nil {
			return fmt.Errorf("monitor de saude: %w", err)
		}
		return nil
	case err := <-chatInternoErr:
		if err != nil {
			return fmt.Errorf("poller de chat interno: %w", err)
		}
		return nil
	case err := <-alertaErr:
		if err != nil {
			return fmt.Errorf("monitor de alerta de volume: %w", err)
		}
		return nil
	case err := <-retencaoErr:
		if err != nil {
			return fmt.Errorf("monitor de retencao: %w", err)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown do servidor http: %w", err)
	}

	if err := <-serverErr; err != nil {
		return fmt.Errorf("servidor http: %w", err)
	}

	// worker.Executar termina o ciclo em andamento e retorna sozinho --
	// nao precisa de outro timeout aqui (ver Config.TimeoutCiclo).
	if err := <-workerErr; err != nil {
		return fmt.Errorf("outbox worker: %w", err)
	}

	if err := <-saudeErr; err != nil {
		return fmt.Errorf("monitor de saude: %w", err)
	}

	if err := <-chatInternoErr; err != nil {
		return fmt.Errorf("poller de chat interno: %w", err)
	}

	if err := <-alertaErr; err != nil {
		return fmt.Errorf("monitor de alerta de volume: %w", err)
	}

	if err := <-retencaoErr; err != nil {
		return fmt.Errorf("monitor de retencao: %w", err)
	}

	slog.Info("gateway encerrado com sucesso")
	return nil
}
