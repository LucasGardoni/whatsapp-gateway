package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/mensagem"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
)

// Mensagens recebe o envio manual do corretor, vindo do CRM (fase 7).
// Grava em 'pendente' -- quem entrega ao WhatsApp continua sendo o
// outbox (fase 3), sem caminho novo que contorne o DLP (secao 10,
// diretriz 10).
//
// Desde a fase 6 do barramento esta rota e uma casca: ela decodifica o
// corpo legado e chama o MESMO entregador que POST /v1/mensagens com
// canal=whatsapp. Nao ha aqui nenhuma regra que /v1 nao tenha, e e assim
// que o aceite da fase ("produz exatamente o mesmo resultado") deixa de
// depender de duas implementacoes ficarem iguais por disciplina.
//
// Sai inteira na fase 8, quando o CRM migrar para /v1/mensagens.
type Mensagens struct {
	pool       *pgxpool.Pool
	entregador entregadorWhatsApp
	// Hub e opcional -- se nil, a mensagem e criada normalmente mas
	// ninguem e notificado em tempo real (equivalente a nao ter SSE
	// configurado ainda).
	Hub *sse.Hub
}

func NovoMensagens(pool *pgxpool.Pool, midiaDir string) *Mensagens {
	return &Mensagens{pool: pool, entregador: entregadorWhatsApp{midiaDir: midiaDir}}
}

type criarMensagemResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// Criar aceita texto e midia (fase 3). Para midia, texto e a legenda e e
// opcional; para texto, ele e o proprio conteudo e e obrigatorio.
func (h *Mensagens) Criar(w http.ResponseWriter, r *http.Request) {
	var req mensagem.Requisicao
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	// a rota legada e WhatsApp por definicao: `canal` no corpo nao existia
	// quando ela foi escrita, e aceita-lo agora daria duas formas de
	// chegar ao canal interno, uma delas sem escopo de aplicacao.
	req.Canal = mensagem.CanalWhatsApp

	// procedencia opcional: o GATEWAY_SERVICE_TOKEN unico nao identifica
	// aplicacao. Some na fase 8 com o proprio token.
	var app *middleware.Aplicacao
	if autenticada, ok := middleware.AplicacaoDoContexto(r.Context()); ok {
		app = &autenticada
	}

	entrega, err := entregar(r.Context(), h.pool, h.Hub, h.entregador, app, req)
	if err != nil {
		responderErro(w, err, "rota", "/api/mensagens", "conversa_id", req.ConversaID)
		return
	}

	responderJSON(w, criarMensagemResponse{ID: entrega.ID, Status: entrega.Status})
}
