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
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Mensagens recebe o envio manual do corretor, vindo do CRM (fase 7).
// Grava em 'pendente' -- quem entrega ao WhatsApp continua sendo o
// outbox (fase 3), sem caminho novo que contorne o DLP (secao 10,
// diretriz 10).
type Mensagens struct {
	pool *pgxpool.Pool
	// midiaDir e a raiz a que todo midia_caminho fica confinado (P2-18).
	// Validado ja na criacao, e nao so na hora do envio, pra o corretor
	// receber o erro na hora em vez de a mensagem morrer no outbox.
	midiaDir string
	// Hub e opcional -- se nil, a mensagem e criada normalmente mas
	// ninguem e notificado em tempo real (equivalente a nao ter SSE
	// configurado ainda).
	Hub *sse.Hub
}

func NovoMensagens(pool *pgxpool.Pool, midiaDir string) *Mensagens {
	return &Mensagens{pool: pool, midiaDir: midiaDir}
}

// tiposMidiaAceitos sao os valores de mensagem.tipo que levam arquivo. A
// lista casa com o CHECK do schema (migration 00003) e com o switch de
// camposEnvioMidia no cliente da z-api -- se divergir, a mensagem e aceita
// aqui e morre no outbox como "tipo nao suportado", que e o pior lugar
// para descobrir.
var tiposMidiaAceitos = map[string]bool{
	"imagem":    true,
	"audio":     true,
	"video":     true,
	"documento": true,
}

type criarMensagemRequest struct {
	ConversaID int64 `json:"conversa_id"`
	// Tipo vazio vira "texto" -- mantem compativel quem ja chamava so com
	// conversa_id e texto.
	Tipo         string `json:"tipo"`
	Texto        string `json:"texto"`
	MidiaCaminho string `json:"midia_caminho"`
}

type criarMensagemResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// Criar aceita texto e midia (fase 3). Para midia, texto e a legenda e e
// opcional; para texto, ele e o proprio conteudo e e obrigatorio.
func (h *Mensagens) Criar(w http.ResponseWriter, r *http.Request) {
	var req criarMensagemRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	req.Texto = strings.TrimSpace(req.Texto)
	req.Tipo = strings.TrimSpace(req.Tipo)
	req.MidiaCaminho = strings.TrimSpace(req.MidiaCaminho)

	if req.Tipo == "" {
		req.Tipo = "texto"
	}
	if req.ConversaID == 0 {
		http.Error(w, "conversa_id e obrigatorio", http.StatusBadRequest)
		return
	}

	switch {
	case req.Tipo == "texto":
		if req.Texto == "" {
			http.Error(w, "texto e obrigatorio para tipo texto", http.StatusBadRequest)
			return
		}
		if req.MidiaCaminho != "" {
			http.Error(w, "midia_caminho nao se aplica ao tipo texto", http.StatusBadRequest)
			return
		}
	case tiposMidiaAceitos[req.Tipo]:
		if req.MidiaCaminho == "" {
			http.Error(w, "midia_caminho e obrigatorio para tipo "+req.Tipo, http.StatusBadRequest)
			return
		}
		// confinado a MIDIA_DIR aqui, na entrada, e nao so no outbox
		// (P2-18/D-3): assim o corretor ve o erro na resposta em vez de a
		// mensagem entrar na fila e falhar em silencio depois.
		if _, err := midia.ResolverDentroDe(h.midiaDir, req.MidiaCaminho); err != nil {
			slog.Warn("mensagens: midia_caminho recusado", "conversa_id", req.ConversaID, "erro", err)
			http.Error(w, "midia_caminho fora do diretorio de midia permitido", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "tipo invalido: use texto, imagem, audio, video ou documento", http.StatusBadRequest)
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
		ConversaID:   req.ConversaID,
		Tipo:         req.Tipo,
		Texto:        naoVazio(req.Texto),
		MidiaCaminho: naoVazio(req.MidiaCaminho),
	})
	if err != nil {
		slog.Error("mensagens: criar mensagem de saida", "conversa_id", req.ConversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// o hash cobre tipo e caminho reais, nao "texto"/"" fixos: senao duas
	// mensagens com a mesma legenda e arquivos diferentes teriam o mesmo
	// elo, e a cadeia deixaria de provar o que foi de fato enviado.
	if err := auditoria.RegistrarHash(ctx, queries, mensagem.ID,
		auditoria.CamposMensagem(mensagem.ID, mensagem.ConversaID, "saida", req.Tipo, req.Texto, req.MidiaCaminho, "")...,
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
