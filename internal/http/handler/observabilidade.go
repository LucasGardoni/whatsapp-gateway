package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/observabilidade"
)

// Observabilidade serve o painel de saude e desempenho do gateway:
//
//	GET /api/observabilidade        retrato completo + diagnostico
//	GET /api/observabilidade/serie  serie das ultimas 2h, passo de 15s
//	GET /health/ready               prontidao (banco + tempo real), publica
//
// As duas primeiras ficam atras de aplicacao.pode_ler_metricas, como
// /metrics: mostram o trafego de todas as aplicacoes. Os numeros sao desta
// instancia (instancia.id no retrato).
type Observabilidade struct {
	coletor   *observabilidade.Coletor
	banco     *observabilidade.Banco
	tempoReal observabilidade.FonteEscutador
}

func NovoObservabilidade(coletor *observabilidade.Coletor, banco *observabilidade.Banco, tempoReal observabilidade.FonteEscutador) *Observabilidade {
	return &Observabilidade{coletor: coletor, banco: banco, tempoReal: tempoReal}
}

func podeLerMetricas(w http.ResponseWriter, r *http.Request) bool {
	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		http.Error(w, "nao autorizado", http.StatusUnauthorized)
		return false
	}
	if !app.PodeLerMetricas {
		http.Error(w, "esta aplicacao nao pode ler metricas", http.StatusForbidden)
		return false
	}
	return true
}

func (h *Observabilidade) Retrato(w http.ResponseWriter, r *http.Request) {
	if !podeLerMetricas(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	responderJSON(w, h.coletor.Retrato(r.Context()))
}

func (h *Observabilidade) Serie(w http.ResponseWriter, r *http.Request) {
	if !podeLerMetricas(w, r) {
		return
	}
	minutos, err := strconv.Atoi(r.URL.Query().Get("minutos"))
	if err != nil || minutos <= 0 || minutos > 120 {
		minutos = 60
	}
	w.Header().Set("Cache-Control", "no-store")
	responderJSON(w, map[string]any{
		"passo_s": observabilidade.IntervaloSerie.Seconds(),
		"pontos":  h.coletor.Serie(minutos),
	})
}

// Prontidao responde 503 quando a instancia esta de pe mas nao serve:
// banco fora ou tempo real parado. /health continua sendo so "o processo
// responde", para o balanceador nao derrubar a instancia por soluco do banco.
// Publica e sem detalhe alem de dois booleanos.
func (h *Observabilidade) Prontidao(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	banco := h.banco != nil && h.banco.Ping(ctx) == nil
	tempoReal := h.tempoReal == nil || h.tempoReal.Estado().Conectado

	status, codigo := "ok", http.StatusOK
	if !banco || !tempoReal {
		status, codigo = "degradado", http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(codigo)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "banco": banco, "tempo_real": tempoReal})
}
