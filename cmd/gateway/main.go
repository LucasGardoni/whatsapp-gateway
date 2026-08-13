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

	"github.com/LucasGardoni/whatsapp-gateway/internal/config"
	"github.com/LucasGardoni/whatsapp-gateway/internal/dlp"
	httpserver "github.com/LucasGardoni/whatsapp-gateway/internal/http"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/handler"
	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/outbox"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
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

	// hub e tokenStore sao o lado go do tempo real do CRM (fase 7) -- um
	// so processo, sem pub/sub distribuido (secao 1: instancia unica).
	hub := sse.NovoHub()
	tokenStore := sse.NovoTokenStore()

	worker := outbox.NovoWorker(queries, zapiCliente, motorDLP, outbox.Config{})
	worker.Hub = hub

	monitorSaude := saude.NovoMonitor(zapiCliente, queries, saude.Config{NomeProvedor: "zapi"})

	baixador := midia.NovoBaixador(cfg.MidiaDir)
	webhookZAPI := handler.NovoWebhookZAPI(pool, baixador)
	webhookZAPI.Hub = hub

	identidadeCliente := identidade.NovoCliente(cfg.ZAPIInstanceID, cfg.ZAPIInstanceToken, cfg.ZAPIClientToken)
	disparo := handler.NovoDisparo(pool, identidadeCliente, cfg.PublicBaseURL)
	transbordo := handler.NovoTransbordo(pool)
	mensagens := handler.NovoMensagens(pool)
	mensagens.Hub = hub
	sessoesSSE := handler.NovoSessoesSSE(tokenStore)
	eventos := handler.NovoEventos(hub, tokenStore)
	zapiAdmin := handler.NovoZAPIAdmin(zapiCliente)

	router := httpserver.NovoRouter(webhookZAPI, disparo, transbordo, mensagens, sessoesSSE, eventos, zapiAdmin, cfg.GatewayServiceToken)

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
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

	slog.Info("gateway encerrado com sucesso")
	return nil
}
