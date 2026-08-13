// package handler concentra os handlers HTTP do gateway. webhook_zapi.go
// cobre os tres webhooks da Z-API usados na v1 (secao 4.4 do plano):
// mensagem recebida, status de mensagem e desconexao da instancia.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/matcher"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// tamanhoMaximoPayload limita o corpo do webhook -- e so metadados e texto,
// midia vem por URL separada (secao 4.6), nao ha motivo pra corpo grande.
const tamanhoMaximoPayload = 5 << 20 // 5MB

// timeoutProcessamento bound o processamento assincrono de uma mensagem
// recebida (matcher + download de midia + escrita no banco).
const timeoutProcessamento = 30 * time.Second

type WebhookZAPI struct {
	pool     *pgxpool.Pool
	baixador *midia.Baixador
	// Hub e opcional -- se nil, o webhook processa normalmente mas ninguem
	// e notificado em tempo real (fase 7).
	Hub *sse.Hub
}

func NovoWebhookZAPI(pool *pgxpool.Pool, baixador *midia.Baixador) *WebhookZAPI {
	return &WebhookZAPI{pool: pool, baixador: baixador}
}

// OnMessageReceived responde 200 rapido e processa depois (secao 10,
// diretriz "responde 200 rapido, processa depois"). O payload bruto e
// gravado antes de qualquer parse -- se essa gravacao falhar, respondemos
// erro para a z-api reenviar; nada mais roda sem o bruto persistido.
func (h *WebhookZAPI) OnMessageReceived(w http.ResponseWriter, r *http.Request) {
	corpo, err := io.ReadAll(io.LimitReader(r.Body, tamanhoMaximoPayload))
	if err != nil {
		http.Error(w, "erro ao ler corpo", http.StatusBadRequest)
		return
	}

	queries := store.New(h.pool)
	payloadBrutoID, err := queries.InserirLeadPayloadBruto(r.Context(), store.InserirLeadPayloadBrutoParams{
		Origem:  "zapi",
		Payload: corpo,
	})
	if err != nil {
		slog.Error("webhook zapi: falha ao persistir payload bruto", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))

	// contexto novo -- r.Context() e cancelado quando o handler retorna,
	// mas o processamento continua depois da resposta.
	go h.processarMensagemRecebida(payloadBrutoID, corpo)
}

func (h *WebhookZAPI) processarMensagemRecebida(payloadBrutoID int64, corpo []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutProcessamento)
	defer cancel()

	var payload zapi.PayloadRecebido
	if err := json.Unmarshal(corpo, &payload); err != nil {
		slog.Error("webhook zapi: payload invalido", "payload_bruto_id", payloadBrutoID, "erro", err)
		return
	}

	if payload.FromMe || payload.IsGroup || payload.IsNewsletter || payload.IsStatusReply {
		return
	}

	tipo, texto, midiaURL, downloadErro := classificarConteudo(payload)

	var midiaCaminho string
	switch {
	case downloadErro != "":
		slog.Warn("webhook zapi: midia indisponivel (downloadError da z-api)", "mensagem_id", payload.MessageID, "erro", downloadErro)
	case midiaURL != "":
		caminho, err := h.baixador.Baixar(ctx, midiaURL, payload.MessageID)
		if err != nil {
			slog.Warn("webhook zapi: falha ao baixar midia, tratando como indisponivel", "mensagem_id", payload.MessageID, "erro", err)
		} else {
			midiaCaminho = caminho
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("webhook zapi: iniciar transacao", "erro", err)
		return
	}
	defer tx.Rollback(ctx)

	queries := store.New(tx)

	chatLidCandidato := payload.ChatLid
	if chatLidCandidato == "" {
		chatLidCandidato = payload.SenderLid
	}
	nomeExibido := payload.SenderName
	if nomeExibido == "" {
		nomeExibido = payload.ChatName
	}

	resultado, err := matcher.Resolver(ctx, queries, matcher.Entrada{
		ChatLid:     chatLidCandidato,
		Phone:       payload.Phone,
		Texto:       texto,
		NomeExibido: nomeExibido,
	})
	if err != nil {
		slog.Error("webhook zapi: matcher", "mensagem_id", payload.MessageID, "erro", err)
		return
	}

	if err := queries.AtualizarLeadDoPayloadBruto(ctx, store.AtualizarLeadDoPayloadBrutoParams{
		ID:     payloadBrutoID,
		LeadID: &resultado.LeadID,
	}); err != nil {
		slog.Error("webhook zapi: atualizar lead do payload bruto", "erro", err)
		return
	}

	// conversa nascida de anuncio click-to-whatsapp -- atribuicao de
	// campanha de graca (secao 4.5, fase 11). So preenche se ainda estiver
	// vazio: a query com COALESCE garante que a primeira mensagem com esse
	// dado e que vale, mesmo que o lead ja exista ha mais tempo.
	if payload.ExternalAdReply != nil && (payload.ExternalAdReply.SourceID != "" || payload.ExternalAdReply.CtwaClid != "") {
		if err := queries.DefinirAtribuicaoCampanhaDoLead(ctx, store.DefinirAtribuicaoCampanhaDoLeadParams{
			ID:         resultado.LeadID,
			AdSourceID: naoVazio(payload.ExternalAdReply.SourceID),
			CtwaClid:   naoVazio(payload.ExternalAdReply.CtwaClid),
		}); err != nil {
			slog.Error("webhook zapi: definir atribuicao de campanha", "lead_id", resultado.LeadID, "erro", err)
		}
	}

	conversa, err := queries.BuscarConversaAbertaPorLead(ctx, resultado.LeadID)
	if errors.Is(err, pgx.ErrNoRows) {
		conversa, err = queries.CriarConversa(ctx, resultado.LeadID)
	}
	if err != nil {
		slog.Error("webhook zapi: obter conversa", "lead_id", resultado.LeadID, "erro", err)
		return
	}

	mensagemID, err := queries.InserirMensagemEntrada(ctx, store.InserirMensagemEntradaParams{
		ConversaID:    conversa.ID,
		Tipo:          tipo,
		Texto:         naoVazio(texto),
		MidiaCaminho:  naoVazio(midiaCaminho),
		Provedor:      "zapi",
		ProvedorMsgID: naoVazio(payload.MessageID),
		PayloadBruto:  corpo,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Info("webhook zapi: mensagem duplicada, ignorando", "provedor_msg_id", payload.MessageID)
		return
	}
	if err != nil {
		slog.Error("webhook zapi: inserir mensagem", "erro", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("webhook zapi: commit", "erro", err)
		return
	}

	// publica so depois do commit -- um corretor nao pode ser notificado
	// de uma mensagem que a transacao acabou descartando.
	if h.Hub != nil {
		h.Hub.Publicar(conversa.CorretorID, sse.Evento{
			Tipo:       sse.EventoMensagemNova,
			MensagemID: mensagemID,
			ConversaID: conversa.ID,
			Status:     "pendente",
		})
	}
}

// classificarConteudo decide o tipo (secao 7 -- CHECK constraint de
// mensagem.tipo) e extrai texto/URL de midia do payload. Tipo nao
// suportado cai em 'outro' -- o bruto ja foi persistido antes disso.
func classificarConteudo(p zapi.PayloadRecebido) (tipo, texto, midiaURL, downloadErro string) {
	switch {
	case p.Text != nil:
		return "texto", p.Text.Message, "", ""
	case p.Image != nil:
		return "imagem", p.Image.Caption, p.Image.URL, p.Image.DownloadError
	case p.Audio != nil:
		return "audio", "", p.Audio.URL, ""
	case p.Video != nil:
		return "video", p.Video.Caption, p.Video.URL, ""
	case p.Document != nil:
		return "documento", "", p.Document.URL, ""
	default:
		return "outro", "", "", ""
	}
}

// OnMessageStatus atualiza enviada -> entregue -> lida. Um callback pode
// trazer varios ids de uma vez.
func (h *WebhookZAPI) OnMessageStatus(w http.ResponseWriter, r *http.Request) {
	var payload zapi.PayloadStatusMensagem
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&payload); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}

	statusInterno, ok := mapearStatus(payload.Status)
	if !ok {
		slog.Info("webhook zapi: status ignorado", "status_zapi", payload.Status)
		w.WriteHeader(http.StatusOK)
		return
	}

	queries := store.New(h.pool)
	for _, id := range payload.IDs {
		id := id
		atualizadas, err := queries.AtualizarStatusMensagemPorProvedorMsgID(r.Context(), store.AtualizarStatusMensagemPorProvedorMsgIDParams{
			ProvedorMsgID: &id,
			Status:        statusInterno,
		})
		if err != nil {
			slog.Error("webhook zapi: atualizar status da mensagem", "provedor_msg_id", id, "erro", err)
			continue
		}
		if len(atualizadas) == 0 {
			slog.Warn("webhook zapi: status recebido para mensagem desconhecida", "provedor_msg_id", id)
			continue
		}
		if h.Hub == nil {
			continue
		}
		for _, m := range atualizadas {
			h.Hub.Publicar(m.CorretorID, sse.Evento{
				Tipo:       sse.EventoMensagemStatus,
				MensagemID: m.ID,
				ConversaID: m.ConversaID,
				Status:     m.Status,
			})
		}
	}

	w.WriteHeader(http.StatusOK)
}

// mapearStatus traduz o vocabulario da z-api para o CHECK constraint de
// mensagem.status (secao 7). READ_BY_ME e PLAYED nao tem equivalente no
// nosso modelo -- ignorados, nao sao erro.
func mapearStatus(statusZAPI string) (string, bool) {
	switch statusZAPI {
	case "SENT":
		return "enviada", true
	case "RECEIVED":
		return "entregue", true
	case "READ":
		return "lida", true
	default:
		return "", false
	}
}

// OnWhatsappDisconnected alimenta provedor_saude -- consumido pelo
// dashboard de saude da fase 9.
func (h *WebhookZAPI) OnWhatsappDisconnected(w http.ResponseWriter, r *http.Request) {
	var payload zapi.PayloadDesconexao
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&payload); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}

	queries := store.New(h.pool)
	if err := queries.RegistrarSaudeProvedor(r.Context(), store.RegistrarSaudeProvedorParams{
		Provedor:   "zapi",
		Conectado:  false,
		UltimoErro: naoVazio(payload.Error),
	}); err != nil {
		slog.Error("webhook zapi: registrar saude do provedor", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func naoVazio(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
