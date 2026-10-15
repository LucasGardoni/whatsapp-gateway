package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/auditoria"
	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// autorCliente marca a alteracao feita pelo contato no aparelho dele.
const autorCliente = "cliente"

// idCitado e o id da mensagem citada pelo contato.
func idCitado(p zapi.PayloadRecebido) string {
	if p.ReferenceMessageID != "" {
		return p.ReferenceMessageID
	}
	if p.ReferencedMessage != nil {
		return p.ReferencedMessage.MessageID
	}
	return ""
}

// idsCandidatos sao os ids que podem apontar a mensagem original de uma
// edicao ou revogacao. A edicao costuma vir com o MESMO messageId da
// original; a revogacao, com a original na referencia. A Fase 0 do plano
// confirma qual -- ate la, tenta todos.
func idsCandidatos(p zapi.PayloadRecebido, acao string) []string {
	var ids []string
	adicionar := func(id string) {
		if id == "" {
			return
		}
		for _, j := range ids {
			if j == id {
				return
			}
		}
		ids = append(ids, id)
	}
	if acao == acaoEdicao {
		adicionar(p.MessageID)
	}
	adicionar(p.ReferenceMessageID)
	if p.ReferencedMessage != nil {
		adicionar(p.ReferencedMessage.MessageID)
	}
	adicionar(p.MessageID)
	return ids
}

// alteracaoDoCliente grava a edicao ou a exclusao que o contato fez numa
// mensagem que ele mandou. false = original nao achada, nada gravado.
func (h *WebhookZAPI) alteracaoDoCliente(cx caixa.Caixa, payloadBrutoID int64, corpo []byte, payload zapi.PayloadRecebido, acao string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutProcessamento)
	defer cancel()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("webhook zapi: alteracao: iniciar transacao", "erro", err)
		return true
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)

	var original store.BuscarMensagemPorProvedorMsgIDNaCaixaRow
	achada := false
	for _, id := range idsCandidatos(payload, acao) {
		id := id
		m, err := q.BuscarMensagemPorProvedorMsgIDNaCaixa(ctx, store.BuscarMensagemPorProvedorMsgIDNaCaixaParams{ProvedorMsgID: &id, CaixaID: cx.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			slog.Error("webhook zapi: alteracao: buscar original", "provedor_msg_id", id, "erro", err)
			return true
		}
		if m.Direcao == "entrada" {
			original, achada = m, true
			break
		}
	}
	if !achada {
		slog.Warn("webhook zapi: alteracao do contato sem mensagem original", "acao", acao, "payload_bruto_id", payloadBrutoID, "message_id", payload.MessageID)
		return false
	}

	alteracao := auditoria.Alteracao{MensagemID: original.ID, Acao: acao, Autor: autorCliente, ProvedorMsgID: payload.MessageID}

	switch acao {
	case acaoExclusao:
		if original.ExcluidaEm.Valid {
			return true
		}
	case acaoEdicao:
		_, texto, _, _ := classificarConteudo(payload)
		texto = strings.TrimSpace(texto)
		if texto == "" {
			slog.Warn("webhook zapi: edicao do contato sem texto", "payload_bruto_id", payloadBrutoID)
			return true
		}
		ultimo, err := q.UltimoTextoEditado(ctx, original.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("webhook zapi: alteracao: ultimo texto", "mensagem_id", original.ID, "erro", err)
			return true
		}
		if err == nil && deref(ultimo) == texto {
			return true // callback repetido
		}
		alteracao.Texto = texto
	}

	if _, err := gravarAlteracao(ctx, q, alteracao, corpo); err != nil {
		slog.Error("webhook zapi: alteracao: gravar", "mensagem_id", original.ID, "erro", err)
		return true
	}

	tipoEvento := sse.EventoMensagemEditada
	if acao == acaoExclusao {
		autor := autorCliente
		if _, err := q.MarcarMensagemExcluida(ctx, store.MarcarMensagemExcluidaParams{ID: original.ID, ExcluidaPor: &autor}); err != nil {
			slog.Error("webhook zapi: alteracao: marcar excluida", "mensagem_id", original.ID, "erro", err)
			return true
		}
		tipoEvento = sse.EventoMensagemExcluida
	} else if err := q.MarcarMensagemEditada(ctx, original.ID); err != nil {
		slog.Error("webhook zapi: alteracao: marcar editada", "mensagem_id", original.ID, "erro", err)
		return true
	}

	if err := h.EventosWhatsApp.Registrar(ctx, q, cx.Codigo, original.CorretorID, sse.Evento{
		Tipo:       tipoEvento,
		MensagemID: original.ID,
		ConversaID: original.ConversaID,
	}); err != nil {
		slog.Error("webhook zapi: alteracao: registrar evento", "mensagem_id", original.ID, "erro", err)
		return true
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("webhook zapi: alteracao: commit", "erro", err)
	}
	return true
}
