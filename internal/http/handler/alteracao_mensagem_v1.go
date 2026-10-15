package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/LucasGardoni/whatsapp-gateway/internal/auditoria"
	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/dlp"
	"github.com/LucasGardoni/whatsapp-gateway/internal/eventos"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Janelas do WhatsApp para mexer numa mensagem ja enviada. Nao estao na doc
// da z-api; fora delas a z-api pode devolver 200 e o aparelho ignorar, por
// isso a recusa e aqui, antes de chamar.
const (
	janelaEdicao   = 15 * time.Minute
	janelaExclusao = 48 * time.Hour
	// timeoutAlteracao limita a chamada sincrona ao provedor: a linha da
	// mensagem fica travada enquanto isso.
	timeoutAlteracao = 20 * time.Second
)

const (
	acaoEdicao   = "edicao"
	acaoExclusao = "exclusao"
)

// statusEntregue sao os status em que a mensagem ja existe no WhatsApp do
// contato -- so ai ha o que editar ou apagar.
var statusEntregue = map[string]bool{"enviada": true, "entregue": true, "lida": true}

// alteracaoMensagens e o que editar/excluir precisa alem da leitura: o
// provedor de cada caixa, o DLP e a entrega dos eventos.
type alteracaoMensagens struct {
	caixas  editoresDaCaixa
	dlp     *dlp.Motor
	eventos eventos.WhatsApp
}

// editoresDaCaixa e o pedaco de caixa.Registro que a alteracao usa.
type editoresDaCaixa interface {
	PorID(ctx context.Context, id int64) (caixa.Caixa, error)
	Editor(c caixa.Caixa) (provedor.Editor, bool)
}

// ComAlteracao liga PATCH/DELETE de mensagem. Sem ela as rotas respondem 501.
func (h *ConversasV1) ComAlteracao(caixas editoresDaCaixa, motor *dlp.Motor, ev eventos.WhatsApp) *ConversasV1 {
	h.alteracao = &alteracaoMensagens{caixas: caixas, dlp: motor, eventos: ev}
	return h
}

type editarMensagemRequest struct {
	Texto        string `json:"texto"`
	Autor        string `json:"autor"`
	DLPIsentoPor string `json:"dlp_isento_por"`
}

type alteracaoResponse struct {
	ID         int64  `json:"id"`
	MensagemID int64  `json:"mensagem_id"`
	Acao       string `json:"acao"`
	CriadoEm   string `json:"criado_em"`
}

// Editar atende PATCH /v1/conversas/{id}/mensagens/{mensagem_id}: troca o
// texto de uma mensagem nossa no WhatsApp do contato. O original fica em
// mensagem.texto; o novo vira linha em mensagem_alteracao.
func (h *ConversasV1) Editar(w http.ResponseWriter, r *http.Request) {
	app, conversaID, mensagemID, ok := h.idsDaAlteracao(w, r)
	if !ok {
		return
	}

	var req editarMensagemRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	texto := strings.TrimSpace(req.Texto)
	autor := strings.TrimSpace(req.Autor)
	if texto == "" {
		http.Error(w, "texto e obrigatorio", http.StatusBadRequest)
		return
	}
	if autor == "" || len(autor) > tamanhoMaximoAutor {
		http.Error(w, "autor e obrigatorio", http.StatusBadRequest)
		return
	}
	isento := strings.TrimSpace(req.DLPIsentoPor) != ""
	if isento && !app.PodeIsentarDLP {
		http.Error(w, "aplicacao sem permissao para isentar o dlp", http.StatusForbidden)
		return
	}

	h.alterar(w, r, conversaID, mensagemID, func(ctx context.Context, q *store.Queries, m store.BuscarMensagemParaAlterarRow, editor provedor.Editor) (store.InserirAlteracaoMensagemRow, error) {
		if m.Tipo != "texto" {
			return store.InserirAlteracaoMensagemRow{}, recusa(http.StatusConflict, "so mensagem de texto pode ser editada")
		}
		if m.IdadeSegundos > janelaEdicao.Seconds() {
			return store.InserirAlteracaoMensagemRow{}, recusa(http.StatusConflict, "passou o prazo de 15 minutos para editar")
		}
		atual, err := textoAtual(ctx, q, m)
		if err != nil {
			return store.InserirAlteracaoMensagemRow{}, err
		}
		if atual == texto {
			return store.InserirAlteracaoMensagemRow{}, recusa(http.StatusBadRequest, "o texto nao mudou")
		}

		// a edicao passa pelo DLP como o envio: senao seria o atalho para
		// mandar o que o bloqueio barrou.
		veredito := h.alteracao.dlp.Avaliar(texto)
		if isento {
			veredito = veredito.Isento()
		}
		registrarOcorrenciasEdicao(ctx, q, m.ID, veredito)
		if veredito.Bloqueado() {
			return store.InserirAlteracaoMensagemRow{}, recusa(http.StatusUnprocessableEntity, "a edicao foi bloqueada pelo dlp")
		}

		chamada, cancel := context.WithTimeout(ctx, timeoutAlteracao)
		defer cancel()
		if err := editor.Editar(chamada, provedor.Edicao{
			Destinatario:  destinoDaAlteracao(m),
			ProvedorMsgID: deref(m.ProvedorMsgID),
			Texto:         texto,
		}); err != nil {
			slog.Warn("alteracao: provedor recusou a edicao", "mensagem_id", m.ID, "erro", err)
			return store.InserirAlteracaoMensagemRow{}, recusa(http.StatusBadGateway, "o WhatsApp nao aceitou a edicao")
		}

		linha, err := gravarAlteracao(ctx, q, auditoria.Alteracao{MensagemID: m.ID, Acao: acaoEdicao, Texto: texto, Autor: autor}, nil)
		if err != nil {
			return linha, err
		}
		if err := q.MarcarMensagemEditada(ctx, m.ID); err != nil {
			return linha, fmt.Errorf("marcar mensagem %d editada: %w", m.ID, err)
		}
		return linha, nil
	}, acaoEdicao)
}

// Excluir atende DELETE /v1/conversas/{id}/mensagens/{mensagem_id}?autor=:
// apaga para todos no WhatsApp. A linha nunca sai do banco: a exclusao e
// marcada e logada em mensagem_alteracao.
func (h *ConversasV1) Excluir(w http.ResponseWriter, r *http.Request) {
	_, conversaID, mensagemID, ok := h.idsDaAlteracao(w, r)
	if !ok {
		return
	}
	autor := strings.TrimSpace(r.URL.Query().Get("autor"))
	if autor == "" || len(autor) > tamanhoMaximoAutor {
		http.Error(w, "autor e obrigatorio", http.StatusBadRequest)
		return
	}

	h.alterar(w, r, conversaID, mensagemID, func(ctx context.Context, q *store.Queries, m store.BuscarMensagemParaAlterarRow, editor provedor.Editor) (store.InserirAlteracaoMensagemRow, error) {
		if m.IdadeSegundos > janelaExclusao.Seconds() {
			return store.InserirAlteracaoMensagemRow{}, recusa(http.StatusConflict, "passou o prazo de 48 horas para apagar")
		}

		chamada, cancel := context.WithTimeout(ctx, timeoutAlteracao)
		defer cancel()
		if err := editor.Excluir(chamada, destinoDaAlteracao(m), deref(m.ProvedorMsgID)); err != nil {
			slog.Warn("alteracao: provedor recusou a exclusao", "mensagem_id", m.ID, "erro", err)
			return store.InserirAlteracaoMensagemRow{}, recusa(http.StatusBadGateway, "o WhatsApp nao aceitou apagar a mensagem")
		}

		linha, err := gravarAlteracao(ctx, q, auditoria.Alteracao{MensagemID: m.ID, Acao: acaoExclusao, Autor: autor}, nil)
		if err != nil {
			return linha, err
		}
		if _, err := q.MarcarMensagemExcluida(ctx, store.MarcarMensagemExcluidaParams{ID: m.ID, ExcluidaPor: &autor}); err != nil {
			return linha, fmt.Errorf("marcar mensagem %d excluida: %w", m.ID, err)
		}
		return linha, nil
	}, acaoExclusao)
}

type versaoResponse struct {
	ID       int64   `json:"id"`
	Acao     string  `json:"acao"`
	Texto    *string `json:"texto"`
	Autor    string  `json:"autor"`
	CriadoEm string  `json:"criado_em"`
}

type versoesResponse struct {
	MensagemID    int64            `json:"mensagem_id"`
	TextoOriginal *string          `json:"texto_original"`
	Versoes       []versaoResponse `json:"versoes"`
}

// Versoes atende GET /v1/conversas/{id}/mensagens/{mensagem_id}/versoes: o
// original e cada edicao/exclusao, em ordem, com quem e quando.
func (h *ConversasV1) Versoes(w http.ResponseWriter, r *http.Request) {
	conversaID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || conversaID <= 0 {
		http.Error(w, "id de conversa invalido", http.StatusBadRequest)
		return
	}
	mensagemID, err := strconv.ParseInt(chi.URLParam(r, "mensagem_id"), 10, 64)
	if err != nil || mensagemID <= 0 {
		http.Error(w, "id de mensagem invalido", http.StatusBadRequest)
		return
	}

	q := store.New(h.pool)
	original, err := q.BuscarMidiaDaMensagem(r.Context(), store.BuscarMidiaDaMensagemParams{MensagemID: mensagemID, ConversaID: conversaID})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "mensagem nao encontrada nesta conversa", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("alteracao: buscar mensagem", "mensagem_id", mensagemID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	linhas, err := q.ListarAlteracoesDaMensagem(r.Context(), store.ListarAlteracoesDaMensagemParams{MensagemID: mensagemID, ConversaID: conversaID})
	if err != nil {
		slog.Error("alteracao: listar versoes", "mensagem_id", mensagemID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	resp := versoesResponse{MensagemID: mensagemID, TextoOriginal: original.Texto, Versoes: make([]versaoResponse, 0, len(linhas))}
	for _, l := range linhas {
		resp.Versoes = append(resp.Versoes, versaoResponse{
			ID:       l.ID,
			Acao:     l.Acao,
			Texto:    l.Texto,
			Autor:    l.Autor,
			CriadoEm: formatarTimestamp(l.CriadoEm),
		})
	}
	responderJSON(w, resp)
}

// idsDaAlteracao valida o que editar e excluir tem em comum. false ja respondeu.
func (h *ConversasV1) idsDaAlteracao(w http.ResponseWriter, r *http.Request) (middleware.Aplicacao, int64, int64, bool) {
	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		http.Error(w, "nao autorizado", http.StatusUnauthorized)
		return app, 0, 0, false
	}
	if h.alteracao == nil {
		http.Error(w, "edicao de mensagem nao configurada", http.StatusNotImplemented)
		return app, 0, 0, false
	}
	conversaID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || conversaID <= 0 {
		http.Error(w, "id de conversa invalido", http.StatusBadRequest)
		return app, 0, 0, false
	}
	mensagemID, err := strconv.ParseInt(chi.URLParam(r, "mensagem_id"), 10, 64)
	if err != nil || mensagemID <= 0 {
		http.Error(w, "id de mensagem invalido", http.StatusBadRequest)
		return app, 0, 0, false
	}
	return app, conversaID, mensagemID, true
}

type passoAlteracao func(ctx context.Context, q *store.Queries, m store.BuscarMensagemParaAlterarRow, editor provedor.Editor) (store.InserirAlteracaoMensagemRow, error)

// alterar e o caminho comum: trava a mensagem, confere que e nossa e ja
// esta no WhatsApp, roda o passo (que chama o provedor e grava) e publica o
// evento na mesma transacao. So grava depois do provedor aceitar.
func (h *ConversasV1) alterar(w http.ResponseWriter, r *http.Request, conversaID, mensagemID int64, passo passoAlteracao, acao string) {
	ctx := r.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("alteracao: iniciar transacao", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)

	m, err := q.BuscarMensagemParaAlterar(ctx, store.BuscarMensagemParaAlterarParams{MensagemID: mensagemID, ConversaID: conversaID})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "mensagem nao encontrada nesta conversa", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("alteracao: buscar mensagem", "mensagem_id", mensagemID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	if motivo := motivoSemAlteracao(m); motivo != "" {
		http.Error(w, motivo, http.StatusConflict)
		return
	}

	cx, err := h.alteracao.caixas.PorID(ctx, m.CaixaID)
	if err != nil {
		slog.Error("alteracao: caixa da conversa", "conversa_id", conversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	editor, ok := h.alteracao.caixas.Editor(cx)
	if !ok || editor == nil {
		http.Error(w, "o provedor desta caixa nao edita nem apaga mensagem", http.StatusNotImplemented)
		return
	}

	linha, err := passo(ctx, q, m, editor)
	if err != nil {
		responderErro(w, err, "rota", "alteracao de mensagem", "mensagem_id", mensagemID)
		return
	}

	tipoEvento := sse.EventoMensagemEditada
	if acao == acaoExclusao {
		tipoEvento = sse.EventoMensagemExcluida
	}
	if err := h.alteracao.eventos.Registrar(ctx, q, cx.Codigo, m.CorretorID, sse.Evento{
		Tipo:       tipoEvento,
		MensagemID: m.ID,
		ConversaID: m.ConversaID,
	}); err != nil {
		slog.Error("alteracao: registrar evento", "mensagem_id", m.ID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// o provedor ja aceitou: falha aqui deixa o WhatsApp do contato a frente
	// do banco, e o log e o que permite reconciliar.
	if err := tx.Commit(ctx); err != nil {
		slog.Error("alteracao: commit depois do provedor aceitar", "mensagem_id", m.ID, "acao", acao, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	responderJSON(w, alteracaoResponse{ID: linha.ID, MensagemID: m.ID, Acao: acao, CriadoEm: formatarTimestamp(linha.CriadoEm)})
}

// motivoSemAlteracao recusa o que nao e mensagem nossa ja entregue ao WhatsApp.
func motivoSemAlteracao(m store.BuscarMensagemParaAlterarRow) string {
	switch {
	case m.Direcao != "saida":
		return "so mensagem enviada pela empresa pode ser alterada"
	case m.ExcluidaEm.Valid:
		return "mensagem ja apagada"
	case !statusEntregue[m.Status] || m.ProvedorMsgID == nil || *m.ProvedorMsgID == "":
		return "a mensagem ainda nao saiu para o WhatsApp"
	}
	return ""
}

// destinoDaAlteracao e o mesmo destinatario do envio (outbox): @lid primeiro.
func destinoDaAlteracao(m store.BuscarMensagemParaAlterarRow) string {
	if m.ChatLid != nil && *m.ChatLid != "" {
		return *m.ChatLid
	}
	return strings.TrimPrefix(deref(m.TelefoneE164), "+")
}

// textoAtual e a ultima edicao, ou o original.
func textoAtual(ctx context.Context, q *store.Queries, m store.BuscarMensagemParaAlterarRow) (string, error) {
	ultimo, err := q.UltimoTextoEditado(ctx, m.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return deref(m.Texto), nil
	}
	if err != nil {
		return "", fmt.Errorf("ultimo texto da mensagem %d: %w", m.ID, err)
	}
	return deref(ultimo), nil
}

// gravarAlteracao insere a linha e o elo dela na cadeia das mensagens, na
// transacao de q. a.ID e preenchido aqui.
func gravarAlteracao(ctx context.Context, q *store.Queries, a auditoria.Alteracao, payloadBruto []byte) (store.InserirAlteracaoMensagemRow, error) {
	linha, err := q.InserirAlteracaoMensagem(ctx, store.InserirAlteracaoMensagemParams{
		MensagemID:    a.MensagemID,
		Acao:          a.Acao,
		Texto:         naoVazio(a.Texto),
		Autor:         a.Autor,
		ProvedorMsgID: naoVazio(a.ProvedorMsgID),
		PayloadBruto:  payloadBruto,
	})
	if err != nil {
		return linha, fmt.Errorf("inserir alteracao da mensagem %d: %w", a.MensagemID, err)
	}
	a.ID = linha.ID
	if err := auditoria.RegistrarHashAlteracao(ctx, q, linha.ID, auditoria.CamposAlteracao(a)...); err != nil {
		return linha, fmt.Errorf("registrar hash da alteracao %d: %w", linha.ID, err)
	}
	return linha, nil
}

// registrarOcorrenciasEdicao grava avisar/bloquear da edicao no relatorio do
// DLP, contra a mensagem editada (mesmo criterio do outbox).
func registrarOcorrenciasEdicao(ctx context.Context, q *store.Queries, mensagemID int64, veredito dlp.Resultado) {
	for _, oc := range veredito.Ocorrencias {
		if oc.Decisao == dlp.Liberar {
			continue
		}
		var confianca pgtype.Numeric
		_ = confianca.Scan(fmt.Sprintf("%.2f", oc.Confianca))
		if err := q.RegistrarOcorrenciaDLP(ctx, store.RegistrarOcorrenciaDLPParams{
			MensagemID: mensagemID,
			Regra:      oc.Regra,
			Decisao:    string(oc.Decisao),
			Confianca:  confianca,
		}); err != nil {
			slog.Error("alteracao: registrar ocorrencia dlp", "mensagem_id", mensagemID, "regra", oc.Regra, "erro", err)
		}
	}
}
