package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/ingestao"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// timeoutProcessamentoLead bound o processamento assincrono de um lead
// recebido (normalizar + dedup + criar), mesmo padrao do webhook zapi.
const timeoutProcessamentoLead = 30 * time.Second

// chaveParametroDedupJanela mora em `parametro` (secao 7) -- janela
// configuravel sem redeploy, pedida explicitamente na fase 11.
const chaveParametroDedupJanela = "ingestao_dedup_janela_horas"

// Leads cobre a ingestao de leads de fontes externas (fase 11): webhook
// generico por origem e importacao de csv pra base fria. A escrita de
// conversa/mensagem de um lead ja existente continua sendo do matcher
// (fase 4) -- aqui e so o momento em que o lead ainda nao existe.
type Leads struct {
	pool           *pgxpool.Pool
	normalizadores ingestao.Registro
	// VerifyToken autentica o handshake GET exigido pela Meta antes dela
	// aceitar configurar um webhook (contrato da plataforma, nao decisao
	// deste projeto). Vazio == handshake sempre falha (fail closed, mesmo
	// padrao do GATEWAY_SERVICE_TOKEN).
	VerifyToken string
}

func NovoLeads(pool *pgxpool.Pool, normalizadores ingestao.Registro) *Leads {
	return &Leads{pool: pool, normalizadores: normalizadores}
}

// VerificarWebhook responde o handshake de verificacao da Meta
// (hub.mode/hub.verify_token/hub.challenge) -- exigido antes dela aceitar
// mandar qualquer POST pro endpoint. Outras origens nao chamam isso.
func (h *Leads) VerificarWebhook(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if h.VerifyToken == "" || q.Get("hub.mode") != "subscribe" || q.Get("hub.verify_token") != h.VerifyToken {
		http.Error(w, "verificacao invalida", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(q.Get("hub.challenge")))
}

// Webhook responde 200 rapido e processa depois (mesmo padrao do webhook
// zapi, secao 10) -- o bruto e persistido antes de qualquer tentativa de
// normalizar (diretriz 7), pra origem nenhuma perder dado por causa de um
// normalizador que ainda nao existe ou de um payload fora do esperado.
func (h *Leads) Webhook(w http.ResponseWriter, r *http.Request) {
	origem := chi.URLParam(r, "origem")
	if origem == "" {
		http.Error(w, "origem e obrigatoria", http.StatusBadRequest)
		return
	}

	corpo, err := io.ReadAll(io.LimitReader(r.Body, tamanhoMaximoPayload))
	if err != nil {
		http.Error(w, "erro ao ler corpo", http.StatusBadRequest)
		return
	}

	queries := store.New(h.pool)
	payloadBrutoID, err := queries.InserirLeadPayloadBruto(r.Context(), store.InserirLeadPayloadBrutoParams{
		Origem:  origem,
		Payload: corpo,
	})
	if err != nil {
		slog.Error("leads: falha ao persistir payload bruto", "origem", origem, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))

	go h.processar(payloadBrutoID, origem, corpo)
}

func (h *Leads) processar(payloadBrutoID int64, origem string, corpo []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutProcessamentoLead)
	defer cancel()

	normalizador, ok := h.normalizadores[origem]
	if !ok {
		slog.Warn("leads: origem sem normalizador registrado, bruto preservado sem virar lead", "origem", origem)
		return
	}

	entrada, err := normalizador(corpo)
	if err != nil {
		slog.Error("leads: normalizar payload", "origem", origem, "payload_bruto_id", payloadBrutoID, "erro", err)
		return
	}

	queries := store.New(h.pool)
	janela := tempoParametroHoras(ctx, queries, chaveParametroDedupJanela, int(ingestao.JanelaDedupPadrao.Hours()))

	resultado, err := ingestao.ResolverOuCriarLead(ctx, queries, entrada, janela)
	if err != nil {
		slog.Error("leads: resolver lead", "origem", origem, "payload_bruto_id", payloadBrutoID, "erro", err)
		return
	}

	if err := queries.AtualizarLeadDoPayloadBruto(ctx, store.AtualizarLeadDoPayloadBrutoParams{
		ID:     payloadBrutoID,
		LeadID: &resultado.LeadID,
	}); err != nil {
		slog.Error("leads: atualizar lead do payload bruto", "erro", err)
	}
}

// ImportarCSV recebe o arquivo como corpo bruto (Content-Type text/csv) --
// upload feito pela tela do CRM (fase 11, unica perna PHP desta fase: "a
// tela que aciona... o upload do csv"). Origem default "csv-importacao",
// customizavel via query string se o supervisor quiser marcar o lote.
func (h *Leads) ImportarCSV(w http.ResponseWriter, r *http.Request) {
	origem := r.URL.Query().Get("origem")
	if origem == "" {
		origem = "csv-importacao"
	}

	ctx := r.Context()
	queries := store.New(h.pool)
	janela := tempoParametroHoras(ctx, queries, chaveParametroDedupJanela, int(ingestao.JanelaDedupPadrao.Hours()))

	resumo, err := ingestao.ImportarCSV(ctx, io.LimitReader(r.Body, tamanhoMaximoImportacaoCSV), queries, origem, janela)
	if err != nil {
		slog.Error("leads: importar csv", "origem", origem, "erro", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resumo)
}

// tamanhoMaximoImportacaoCSV e bem maior que tamanhoMaximoPayload (webhook)
// -- um upload de base fria pode ter milhares de linhas.
const tamanhoMaximoImportacaoCSV = 20 << 20 // 20MB
