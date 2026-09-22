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
	"github.com/LucasGardoni/whatsapp-gateway/internal/config"
	"github.com/LucasGardoni/whatsapp-gateway/internal/dlp"
	"github.com/LucasGardoni/whatsapp-gateway/internal/eventos"
	httpserver "github.com/LucasGardoni/whatsapp-gateway/internal/http"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/handler"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/ingestao"
	"github.com/LucasGardoni/whatsapp-gateway/internal/metrica"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/outbox"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
	"github.com/LucasGardoni/whatsapp-gateway/internal/retencao"
	"github.com/LucasGardoni/whatsapp-gateway/internal/saude"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

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

	// hub, assinador e escutador sao o lado go do tempo real. O assinador
	// substituiu o TokenStore em memoria na fase 3 do barramento: token
	// assinado por HMAC nao precisa de estado compartilhado. O escutador
	// fechou o outro lado do mesmo teto na fase 7: quem grava a mensagem
	// nao publica mais no hub do proprio processo, e sim uma linha em
	// `evento`; e o escutador, em CADA instancia, que a entrega ao hub
	// dela. Sem ele o gateway grava tudo certo e nenhuma tela se mexe.
	hub := sse.NovoHub()
	assinadorSSE := sse.NovoAssinadorSessao(cfg.SSESigningKey)
	escutadorEventos := eventos.NovoEscutador(cfg.DatabaseURL, queries, hub, eventos.Config{})

	worker := outbox.NovoWorker(queries, zapiCliente, motorDLP, outbox.Config{MidiaDir: cfg.MidiaDir})

	monitorSaude := saude.NovoMonitor(zapiCliente, queries, saude.Config{NomeProvedor: "zapi"})

	// o mesmo monitor cobre os dois alertas de volume: o da empresa
	// (destinatarios distintos, risco de banimento do numero) e o por
	// aplicacao (fase 9, integracao em laco). Um ticker, duas consultas --
	// nao ha por que somar uma goroutine e um canal de erro para a
	// segunda.
	monitorAlerta := alerta.NovoMonitor(queries, alerta.Config{})

	monitorRetencao := retencao.NovoMonitor(queries, retencao.Config{})

	// registro de metricas por aplicacao (fase 9). Em memoria e por
	// instancia -- ver internal/metrica para por que nao vai ao banco.
	registroMetricas := metrica.NovoRegistro()

	baixador := midia.NovoBaixador(cfg.MidiaDir)
	webhookZAPI := handler.NovoWebhookZAPI(pool, baixador)

	identidadeCliente := identidade.NovoCliente(cfg.ZAPIInstanceID, cfg.ZAPIInstanceToken, cfg.ZAPIClientToken)
	disparo := handler.NovoDisparo(pool, identidadeCliente, cfg.PublicBaseURL)
	transbordo := handler.NovoTransbordo(pool)
	mensagensV1 := handler.NovoMensagensV1(pool, cfg.MidiaDir, cfg.LimiteConteudoCifradoBytes, registroMetricas)
	sessoesSSE := handler.NovoSessoesSSE(assinadorSSE)
	eventos := handler.NovoEventos(hub, assinadorSSE, cfg.CORSOrigemCRM, registroMetricas)
	zapiAdmin := handler.NovoZAPIAdmin(zapiCliente)
	canais := handler.NovoCanais(pool)
	leads := handler.NovoLeads(pool, ingestao.RegistroPadrao())
	metricas := handler.NovoMetricas(registroMetricas)
	leads.VerifyToken = cfg.MetaWebhookVerifyToken

	if cfg.WebhookPathSecret == "" {
		slog.Warn("WEBHOOK_PATH_SECRET vazio: os webhooks de entrada respondem 404 e nada entra no gateway. Nao exponha o gateway na internet sem ele")
	}

	if assinadorSSE == nil {
		slog.Warn("SSE_SIGNING_KEY vazia: o tempo real fica desligado (POST /v1/sessoes, POST /api/sessoes-sse e GET /eventos respondem 503). Gere uma com: openssl rand -base64 32")
	}

	// Identidade por aplicacao (barramento, fase 1) -- desde a fase 8, a
	// UNICA autenticacao de servico que existe.
	//
	// Nao ha mais bootstrap por variavel de ambiente: ate a fase 7 o
	// gateway sincronizava aqui o hash do GATEWAY_SERVICE_TOKEN para a
	// aplicacao 'crm', como ponte para o CRM nao trocar de credencial na
	// migracao. A ponte cumpriu o papel e saiu junto com a variavel.
	//
	// Aplicacao nova (ou banco recriado do zero) se registra com
	// scripts/registrar-aplicacao.ps1, que gera o token e grava so o hash.
	autenticadorApp := middleware.NovoAutenticadorAplicacao(queries)

	router := httpserver.NovoRouter(
		webhookZAPI, disparo, transbordo, mensagensV1, sessoesSSE, eventos, zapiAdmin, leads, canais, metricas,
		autenticadorApp, registroMetricas,
		cfg.WebhookPathSecret, cfg.RateLimitPorMinuto, cfg.RateLimitAplicacaoPorMinuto,
	)

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

	eventosErr := make(chan error, 1)
	go func() {
		slog.Info("escutador de eventos iniciado")
		eventosErr <- escutadorEventos.Executar(ctx)
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
	case err := <-eventosErr:
		if err != nil {
			return fmt.Errorf("escutador de eventos: %w", err)
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

	if err := <-eventosErr; err != nil {
		return fmt.Errorf("escutador de eventos: %w", err)
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
