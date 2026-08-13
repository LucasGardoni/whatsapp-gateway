package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/auditoria"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Mensagens recebe o envio manual do corretor, vindo do CRM (fase 7).
// Grava em 'pendente' -- quem entrega ao WhatsApp continua sendo o
// outbox (fase 3), sem caminho novo que contorne o DLP (secao 10,
// diretriz 10).
type Mensagens struct {
	pool *pgxpool.Pool
	// Hub e opcional -- se nil, a mensagem e criada normalmente mas
	// ninguem e notificado em tempo real (equivalente a nao ter SSE
	// configurado ainda).
	Hub *sse.Hub
}

func NovoMensagens(pool *pgxpool.Pool) *Mensagens {
	return &Mensagens{pool: pool}
}

type criarMensagemRequest struct {
	ConversaID int64  `json:"conversa_id"`
	Texto      string `json:"texto"`
}

type criarMensagemResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// Criar so aceita texto na v1 -- envio de midia depende do provedor
// suportar (fase 9, ver plano do gateway).
func (h *Mensagens) Criar(w http.ResponseWriter, r *http.Request) {
	var req criarMensagemRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	req.Texto = strings.TrimSpace(req.Texto)
	if req.ConversaID == 0 || req.Texto == "" {
		http.Error(w, "conversa_id e texto sao obrigatorios", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// tx envolve criar a mensagem e encadear o hash de auditoria (secao 2,
	// defesa no 4, fase 12) -- os dois tem que confirmar juntos, senao um
	// crash entre eles deixa a mensagem sem elo na cadeia.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("mensagens: iniciar transacao", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	queries := store.New(tx)

	conversa, err := queries.BuscarConversaPorID(ctx, req.ConversaID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversa nao encontrada", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("mensagens: buscar conversa", "conversa_id", req.ConversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if conversa.FechadaEm.Valid {
		http.Error(w, "conversa encerrada", http.StatusConflict)
		return
	}

	mensagem, err := queries.CriarMensagemSaida(ctx, store.CriarMensagemSaidaParams{
		ConversaID: req.ConversaID,
		Texto:      &req.Texto,
	})
	if err != nil {
		slog.Error("mensagens: criar mensagem de saida", "conversa_id", req.ConversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	if err := auditoria.RegistrarHash(ctx, queries, mensagem.ID,
		auditoria.CamposMensagem(mensagem.ID, mensagem.ConversaID, "saida", "texto", req.Texto, "", "")...,
	); err != nil {
		slog.Error("mensagens: registrar hash de auditoria", "mensagem_id", mensagem.ID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("mensagens: commit", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// publica so depois do commit -- mesmo padrao do webhook zapi, um
	// corretor nao pode ser notificado de uma mensagem que a transacao
	// acabou descartando.
	if h.Hub != nil {
		h.Hub.Publicar(conversa.CorretorID, sse.Evento{
			Tipo:       sse.EventoMensagemNova,
			MensagemID: mensagem.ID,
			ConversaID: mensagem.ConversaID,
			Status:     mensagem.Status,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(criarMensagemResponse{ID: mensagem.ID, Status: mensagem.Status})
}
