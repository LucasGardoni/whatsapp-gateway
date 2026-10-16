package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/auditoria"
	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Eventos do grupo que mudam a participacao do NOSSO numero. Os demais
// GROUP_* so viram marcador na timeline.
const (
	notificacaoGrupoCriado       = "GROUP_CREATE"
	notificacaoParticipanteAdd   = "GROUP_PARTICIPANT_ADD"
	notificacaoParticipanteConv  = "GROUP_PARTICIPANT_INVITE"
	notificacaoParticipanteRem   = "GROUP_PARTICIPANT_REMOVE"
	notificacaoParticipanteSaiu  = "GROUP_PARTICIPANT_LEAVE"
	prefixoNotificacaoGrupo      = "GROUP_"
	prefixoNotificacaoMembership = "MEMBERSHIP_"
)

// efeitoGrupo e o que um callback de grupo faz com a conversa da caixa.
type efeitoGrupo int

const (
	efeitoNenhum efeitoGrupo = iota
	efeitoEntrou
	efeitoSaiu
)

// processarGrupo grava mensagem e evento de grupo (migration 00031). Grupo nao
// passa pelo matcher nem pelo ciclo de vida do lead: nao e contato, e o id
// '-group' nao e telefone.
//
// fromMe so e descartado em mensagem comum: o envio pela aplicacao ja esta no
// banco. Evento do grupo com fromMe (o proprio numero saiu) precisa entrar.
func (h *WebhookZAPI) processarGrupo(ctx context.Context, cx caixa.Caixa, payloadBrutoID int64, corpo []byte, payload zapi.PayloadRecebido) {
	grupoID := strings.TrimSpace(payload.Phone)
	if grupoID == "" {
		slog.Warn("webhook zapi: grupo sem id, ignorando", "payload_bruto_id", payloadBrutoID)
		return
	}

	evento := ehNotificacaoDeGrupo(payload.Notification)
	if payload.FromMe && !evento {
		return
	}

	if !evento {
		if payload.Notification == zapi.NotificacaoRevogacao {
			h.alteracaoDoCliente(cx, payloadBrutoID, corpo, payload, acaoExclusao)
			return
		}
		if payload.IsEdit && h.alteracaoDoCliente(cx, payloadBrutoID, corpo, payload, acaoEdicao) {
			return
		}
	}

	efeito := efeitoDoEvento(payload)

	var tipo, texto, midiaCaminho string
	if evento {
		tipo = "outro"
	} else {
		var midiaURL, downloadErro string
		tipo, texto, midiaURL, downloadErro = classificarConteudo(payload)
		switch {
		case downloadErro != "":
			slog.Warn("webhook zapi: midia de grupo indisponivel", "mensagem_id", payload.MessageID, "erro", downloadErro)
		case midiaURL != "":
			caminho, err := h.baixador.Baixar(ctx, midiaURL, payload.MessageID)
			if err != nil {
				slog.Warn("webhook zapi: falha ao baixar midia de grupo", "mensagem_id", payload.MessageID, "erro", err)
			} else {
				midiaCaminho = caminho
			}
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("webhook zapi: grupo: iniciar transacao", "erro", err)
		return
	}
	defer tx.Rollback(ctx)
	queries := store.New(tx)

	if err := queries.TravarGrupo(ctx, grupoID); err != nil {
		slog.Error("webhook zapi: travar grupo", "grupo", grupoID, "erro", err)
		return
	}

	leadID, err := leadDoGrupo(ctx, queries, grupoID, strings.TrimSpace(payload.ChatName))
	if err != nil {
		slog.Error("webhook zapi: lead do grupo", "grupo", grupoID, "erro", err)
		return
	}
	if err := queries.AtualizarLeadDoPayloadBruto(ctx, store.AtualizarLeadDoPayloadBrutoParams{ID: payloadBrutoID, LeadID: &leadID}); err != nil {
		slog.Error("webhook zapi: grupo: atualizar lead do payload bruto", "erro", err)
		return
	}

	conversa, criada, err := conversaDoGrupo(ctx, queries, leadID, cx.ID, efeito != efeitoSaiu)
	if err != nil {
		slog.Error("webhook zapi: conversa do grupo", "grupo", grupoID, "erro", err)
		return
	}
	// saida de um grupo que nunca conhecemos: nada a registrar.
	if conversa.ID == 0 {
		if err := tx.Commit(ctx); err != nil {
			slog.Error("webhook zapi: grupo: commit", "erro", err)
		}
		return
	}

	params := store.InserirMensagemEntradaGrupoParams{
		ConversaID:           conversa.ID,
		Tipo:                 tipo,
		Texto:                naoVazio(texto),
		MidiaCaminho:         naoVazio(midiaCaminho),
		Provedor:             "zapi",
		ProvedorMsgID:        naoVazio(payload.MessageID),
		PayloadBruto:         corpo,
		ParticipanteTelefone: naoVazio(strings.TrimSpace(payload.ParticipantPhone)),
		ParticipanteLid:      naoVazio(strings.TrimSpace(payload.ParticipantLid)),
		ParticipanteNome:     naoVazio(strings.TrimSpace(payload.SenderName)),
	}
	if evento {
		params.Notificacao = &payload.Notification
		params.NotificacaoParametros = payload.NotificationParameters
		if params.NotificacaoParametros == nil {
			params.NotificacaoParametros = []string{}
		}
	}

	mensagemID, err := queries.InserirMensagemEntradaGrupo(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Info("webhook zapi: mensagem de grupo duplicada, ignorando", "provedor_msg_id", payload.MessageID)
		return
	}
	if err != nil {
		slog.Error("webhook zapi: inserir mensagem de grupo", "erro", err)
		return
	}

	if ref := idCitado(payload); ref != "" && !evento {
		citada, err := queries.BuscarMensagemPorProvedorMsgIDNaCaixa(ctx, store.BuscarMensagemPorProvedorMsgIDNaCaixaParams{ProvedorMsgID: &ref, CaixaID: cx.ID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			slog.Error("webhook zapi: grupo: buscar mensagem citada", "provedor_msg_id", ref, "erro", err)
			return
		case citada.ConversaID == conversa.ID:
			if err := queries.DefinirRespostaDaMensagem(ctx, store.DefinirRespostaDaMensagemParams{ID: mensagemID, RespondeAID: &citada.ID}); err != nil {
				slog.Error("webhook zapi: grupo: ligar mensagem citada", "mensagem_id", mensagemID, "erro", err)
				return
			}
		}
	}

	if err := auditoria.RegistrarHash(ctx, queries, mensagemID,
		auditoria.CamposMensagem(auditoria.Mensagem{
			ID:            mensagemID,
			ConversaID:    conversa.ID,
			Direcao:       "entrada",
			Tipo:          tipo,
			Texto:         texto,
			MidiaCaminho:  midiaCaminho,
			ProvedorMsgID: payload.MessageID,
			Origem:        origemProvedorZAPI,
		})...,
	); err != nil {
		slog.Error("webhook zapi: grupo: registrar hash", "mensagem_id", mensagemID, "erro", err)
		return
	}

	// o evento de saida fica registrado DENTRO da conversa antes de fecha-la.
	if efeito == efeitoSaiu {
		if _, err := queries.FecharConversa(ctx, conversa.ID); err != nil {
			slog.Error("webhook zapi: fechar conversa do grupo", "conversa_id", conversa.ID, "erro", err)
			return
		}
	}

	if err := h.EventosWhatsApp.Registrar(ctx, queries, cx.Codigo, conversa.CorretorID, sse.Evento{
		Tipo:       sse.EventoMensagemNova,
		MensagemID: mensagemID,
		ConversaID: conversa.ID,
		Status:     "pendente",
	}); err != nil {
		slog.Error("webhook zapi: grupo: registrar evento", "mensagem_id", mensagemID, "erro", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("webhook zapi: grupo: commit", "erro", err)
		return
	}
	if criada {
		slog.Info("webhook zapi: grupo novo na caixa", "caixa", cx.Codigo, "grupo", grupoID, "conversa_id", conversa.ID)
	}
}

func ehNotificacaoDeGrupo(n string) bool {
	return strings.HasPrefix(n, prefixoNotificacaoGrupo) || strings.HasPrefix(n, prefixoNotificacaoMembership) ||
		n == "REVOKED_MEMBERSHIP_REQUESTS"
}

// efeitoDoEvento decide se o callback mexe na participacao do nosso numero.
// O formato de notificationParameters dos PARTICIPANT_* nao esta na doc da
// z-api: aceita o numero (com ou sem DDI/nono digito) em qualquer posicao, e
// o participantPhone quando o proprio numero e o autor (saiu sozinho).
func efeitoDoEvento(p zapi.PayloadRecebido) efeitoGrupo {
	switch p.Notification {
	case notificacaoGrupoCriado:
		return efeitoEntrou
	case notificacaoParticipanteAdd, notificacaoParticipanteConv:
		if contemNumero(p.NotificationParameters, p.ConnectedPhone) {
			return efeitoEntrou
		}
	case notificacaoParticipanteRem, notificacaoParticipanteSaiu:
		if contemNumero(p.NotificationParameters, p.ConnectedPhone) ||
			(p.Notification == notificacaoParticipanteSaiu && len(p.NotificationParameters) == 0 && mesmoNumero(p.ParticipantPhone, p.ConnectedPhone)) {
			return efeitoSaiu
		}
	}
	return efeitoNenhum
}

func contemNumero(lista []string, numero string) bool {
	for _, item := range lista {
		if mesmoNumero(item, numero) {
			return true
		}
	}
	return false
}

// mesmoNumero compara pelo E.164 normalizado (nono digito incluso); fora do
// padrao brasileiro, pelos digitos.
func mesmoNumero(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" || identidade.EhLid(a) || identidade.EhLid(b) {
		return false
	}
	ea, errA := identidade.NormalizarE164(a)
	eb, errB := identidade.NormalizarE164(b)
	if errA == nil && errB == nil {
		return ea == eb
	}
	return apenasDigitos(a) == apenasDigitos(b)
}

func apenasDigitos(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func leadDoGrupo(ctx context.Context, q *store.Queries, grupoID, nome string) (int64, error) {
	lead, err := q.BuscarLeadPorGrupo(ctx, &grupoID)
	if errors.Is(err, pgx.ErrNoRows) {
		criado, err := q.CriarLeadGrupo(ctx, store.CriarLeadGrupoParams{Nome: naoVazio(nome), GrupoID: &grupoID})
		if err != nil {
			return 0, err
		}
		return criado.ID, nil
	}
	if err != nil {
		return 0, err
	}
	if nome != "" {
		if err := q.AtualizarNomeDoGrupo(ctx, store.AtualizarNomeDoGrupoParams{ID: lead.ID, Nome: &nome}); err != nil {
			return 0, err
		}
	}
	return lead.ID, nil
}

// conversaDoGrupo devolve a conversa do grupo na caixa, reaberta. Sem
// conversa e sem abrir (evento de saida de grupo desconhecido) volta ID 0.
func conversaDoGrupo(ctx context.Context, q *store.Queries, leadID, caixaID int64, abrir bool) (store.Conversa, bool, error) {
	conversa, err := q.BuscarUltimaConversaPorLead(ctx, store.BuscarUltimaConversaPorLeadParams{LeadID: leadID, CaixaID: caixaID})
	if errors.Is(err, pgx.ErrNoRows) {
		if !abrir {
			return store.Conversa{}, false, nil
		}
		conversa, err = q.CriarConversa(ctx, store.CriarConversaParams{LeadID: leadID, CaixaID: caixaID})
		return conversa, err == nil, err
	}
	if err != nil {
		return store.Conversa{}, false, err
	}
	if abrir && conversa.FechadaEm.Valid {
		if err := q.ReabrirConversa(ctx, conversa.ID); err != nil {
			return store.Conversa{}, false, err
		}
		conversa.FechadaEm.Valid = false
	}
	return conversa, false, nil
}
