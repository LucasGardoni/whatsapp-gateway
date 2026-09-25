package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// ConversasV1 atende G2/G3/G4/G6 do barramento (docs/PLANO_MULTICAIXA_E_CONVERSAS.md):
// GET /v1/conversas, GET /v1/conversas/{id}/mensagens, POST /v1/conversas e
// POST /v1/conversas/{id}/lida. Devolver o que o gateway mesmo gravou em
// `conversa`/`mensagem` nao e consulta de dominio da aplicacao -- e por
// isso vive aqui, sem nenhum if por aplicacao.
//
// nao_lidas e por aplicacao (G6): entrada acima da ultima leitura que a
// aplicacao autenticada marcou.
type ConversasV1 struct {
	pool *pgxpool.Pool
	// caixas resolve o parametro `caixa` (G1). Codigo que nao casa com caixa
	// ativa e 400, nunca a caixa padrao: um valor errado e bug no chamador,
	// e devolver outra caixa esconderia o bug.
	caixas caixasV1
	// identidade devolve o resolvedor de @lid DA CAIXA: o get-iswhatsapp tem
	// de ser perguntado a instancia que vai conversar. Nil segue sem @lid.
	identidade func(caixa.Caixa) resolvedorLid
}

// caixasV1 e o pedaco de caixa.Registro que as rotas /v1 usam.
type caixasV1 interface {
	PorCodigo(ctx context.Context, codigo string) (caixa.Caixa, error)
}

// resolvedorLid e o pedaco de identidade.Cliente que a abertura usa.
type resolvedorLid interface {
	ResolverLid(ctx context.Context, telefone string) (*identidade.ResultadoLid, error)
}

func NovoConversasV1(pool *pgxpool.Pool, caixas *caixa.Registro) *ConversasV1 {
	return &ConversasV1{
		pool:   pool,
		caixas: caixas,
		identidade: func(c caixa.Caixa) resolvedorLid {
			if cl, ok := caixas.Identidade(c); ok {
				return cl
			}
			return nil
		},
	}
}

// caixaDaRequisicao resolve o codigo pedido; vazio e a caixa padrao. false
// ja respondeu (400 para codigo desconhecido).
func caixaDaRequisicao(w http.ResponseWriter, r *http.Request, caixas caixasV1, codigo string) (caixa.Caixa, bool) {
	cx, err := caixas.PorCodigo(r.Context(), strings.TrimSpace(codigo))
	if errors.Is(err, caixa.ErrDesconhecida) {
		http.Error(w, "caixa desconhecida: "+codigo, http.StatusBadRequest)
		return caixa.Caixa{}, false
	}
	if err != nil {
		slog.Error("v1: resolver caixa", "caixa", codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return caixa.Caixa{}, false
	}
	return cx, true
}

const (
	limitePadraoConversas = 50
	limiteMaximoConversas = 200
	limitePadraoMensagens = 50
	limiteMaximoMensagens = 500
	tamanhoPreviaMaximo   = 120
	layoutTempoResposta   = "2006-01-02T15:04:05"
)

type contatoResponse struct {
	Nome         *string `json:"nome"`
	TelefoneE164 *string `json:"telefone_e164"`
	ChatLid      *string `json:"chat_lid"`
}

type ultimaMensagemResponse struct {
	ID       int64  `json:"id"`
	Direcao  string `json:"direcao"`
	Tipo     string `json:"tipo"`
	Previa   string `json:"previa"`
	Status   string `json:"status"`
	CriadoEm string `json:"criado_em"`
}

type conversaResponse struct {
	ID             int64                   `json:"id"`
	Caixa          string                  `json:"caixa"`
	Contato        contatoResponse         `json:"contato"`
	AbertaEm       string                  `json:"aberta_em"`
	FechadaEm      *string                 `json:"fechada_em"`
	UltimaMensagem *ultimaMensagemResponse `json:"ultima_mensagem"`
	NaoLidas       int                     `json:"nao_lidas"`
}

type listarConversasResponse struct {
	Conversas []conversaResponse `json:"conversas"`
	Cursor    string             `json:"cursor"`
	UltimoID  int64              `json:"ultimo_id"`
}

// cursorConversas e o conteudo do cursor opaco (secao G2/G3 do plano):
// base64 de um JSON pequeno. Opaco para o chamador, mas nao criptografado
// -- nao carrega nada que valha a pena esconder, so a chave de retomada
// da paginacao por keyset.
type cursorConversas struct {
	UltimaAtividade time.Time `json:"a"`
	ID              int64     `json:"id"`
}

func codificarCursor(c cursorConversas) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodificarCursor(s string) (cursorConversas, error) {
	var c cursorConversas
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

// Listar atende GET /v1/conversas.
func (h *ConversasV1) Listar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// sem `caixa`, todas: cada conversa diz a sua no campo `caixa`.
	var caixaID *int64
	if codigo := strings.TrimSpace(q.Get("caixa")); codigo != "" {
		cx, ok := caixaDaRequisicao(w, r, h.caixas, codigo)
		if !ok {
			return
		}
		caixaID = &cx.ID
	}

	estado := strings.TrimSpace(q.Get("estado"))
	if estado == "" {
		estado = "abertas"
	}
	if estado != "abertas" && estado != "fechadas" && estado != "todas" {
		http.Error(w, "estado deve ser abertas, fechadas ou todas", http.StatusBadRequest)
		return
	}

	var desde pgtype.Timestamp
	if bruto := strings.TrimSpace(q.Get("desde")); bruto != "" {
		t, err := time.Parse(time.RFC3339, bruto)
		if err != nil {
			http.Error(w, "desde deve ser um timestamp RFC3339", http.StatusBadRequest)
			return
		}
		desde = pgtype.Timestamp{Time: t, Valid: true}
	}

	limite, ok := inteiroDaQuery(w, r, "limite", limitePadraoConversas)
	if !ok {
		return
	}
	if limite < 1 || limite > limiteMaximoConversas {
		limite = limiteMaximoConversas
	}

	var cursorAtividade pgtype.Timestamp
	var cursorID *int64
	if bruto := strings.TrimSpace(q.Get("cursor")); bruto != "" {
		c, err := decodificarCursor(bruto)
		if err != nil {
			http.Error(w, "cursor invalido", http.StatusBadRequest)
			return
		}
		cursorAtividade = pgtype.Timestamp{Time: c.UltimaAtividade, Valid: true}
		id := c.ID
		cursorID = &id
	}

	// sem aplicacao no contexto (so nos testes que montam o handler sem o
	// middleware) o id 0 nao casa com leitura nenhuma: tudo conta como nao lido.
	app, _ := middleware.AplicacaoDoContexto(r.Context())

	linhas, err := store.New(h.pool).ListarConversas(r.Context(), store.ListarConversasParams{
		AplicacaoID:     app.ID,
		CaixaID:         caixaID,
		Estado:          estado,
		Desde:           desde,
		CursorAtividade: cursorAtividade,
		CursorID:        cursorID,
		Limite:          int32(limite),
	})
	if err != nil {
		slog.Error("conversas v1: listar", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	resp := listarConversasResponse{Conversas: make([]conversaResponse, 0, len(linhas))}

	for _, l := range linhas {
		item := conversaResponse{
			ID:    l.ID,
			Caixa: l.Caixa,
			Contato: contatoResponse{
				Nome:         l.ContatoNome,
				TelefoneE164: l.ContatoTelefone,
				ChatLid:      l.ContatoChatLid,
			},
			AbertaEm: formatarTimestamp(l.AbertaEm),
			NaoLidas: int(l.NaoLidas),
		}
		if l.FechadaEm.Valid {
			s := formatarTimestamp(l.FechadaEm)
			item.FechadaEm = &s
		}
		if l.UltimaMensagemID != nil {
			item.UltimaMensagem = &ultimaMensagemResponse{
				ID:       *l.UltimaMensagemID,
				Direcao:  deref(l.UltimaMensagemDirecao),
				Tipo:     deref(l.UltimaMensagemTipo),
				Previa:   previa(deref(l.UltimaMensagemTipo), l.UltimaMensagemTexto),
				Status:   deref(l.UltimaMensagemStatus),
				CriadoEm: formatarTimestamp(l.UltimaAtividade),
			}
			if *l.UltimaMensagemID > resp.UltimoID {
				resp.UltimoID = *l.UltimaMensagemID
			}
		}
		resp.Conversas = append(resp.Conversas, item)
	}

	// cursor da proxima pagina: a ultima linha desta, na mesma chave de
	// ordenacao da query (ultima_atividade DESC, id DESC). Pagina vazia ou
	// menor que o limite pedido nao tem proxima -- cursor fica vazio, e a
	// aplicacao sabe que chegou ao fim sem precisar comparar tamanhos.
	if len(linhas) == int(limite) {
		ultima := linhas[len(linhas)-1]
		resp.Cursor = codificarCursor(cursorConversas{
			UltimaAtividade: ultima.UltimaAtividade.Time,
			ID:              ultima.ID,
		})
	}

	responderJSON(w, resp)
}

type mensagemConversaResponse struct {
	ID           int64   `json:"id"`
	Direcao      string  `json:"direcao"`
	Tipo         string  `json:"tipo"`
	Texto        *string `json:"texto"`
	MidiaCaminho *string `json:"midia_caminho"`
	Status       string  `json:"status"`
	CriadoEm     string  `json:"criado_em"`
}

type mensagensConversaResponse struct {
	ConversaID int64                      `json:"conversa_id"`
	Mensagens  []mensagemConversaResponse `json:"mensagens"`
	UltimoID   int64                      `json:"ultimo_id"`
}

// Mensagens atende GET /v1/conversas/{id}/mensagens.
func (h *ConversasV1) Mensagens(w http.ResponseWriter, r *http.Request) {
	conversaID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || conversaID <= 0 {
		http.Error(w, "id de conversa invalido", http.StatusBadRequest)
		return
	}

	queries := store.New(h.pool)

	if _, err := queries.BuscarConversaComContatoPorID(r.Context(), conversaID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "conversa nao encontrada", http.StatusNotFound)
			return
		}
		slog.Error("conversas v1: buscar conversa", "conversa_id", conversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	desdeID, ok := inteiroDaQuery(w, r, "desde_id", 0)
	if !ok {
		return
	}
	limite, ok := inteiroDaQuery(w, r, "limite", limitePadraoMensagens)
	if !ok {
		return
	}
	if limite <= 0 || limite > limiteMaximoMensagens {
		limite = limiteMaximoMensagens
	}

	var ateID *int64
	if bruto := strings.TrimSpace(r.URL.Query().Get("ate_id")); bruto != "" {
		v, err := strconv.ParseInt(bruto, 10, 64)
		if err != nil {
			http.Error(w, "ate_id deve ser um numero inteiro", http.StatusBadRequest)
			return
		}
		ateID = &v
	}

	linhas, err := queries.ListarMensagensDaConversa(r.Context(), store.ListarMensagensDaConversaParams{
		ConversaID: conversaID,
		DesdeID:    desdeID,
		AteID:      ateID,
		Limite:     int32(limite),
	})
	if err != nil {
		slog.Error("conversas v1: listar mensagens", "conversa_id", conversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	resp := mensagensConversaResponse{
		ConversaID: conversaID,
		Mensagens:  make([]mensagemConversaResponse, 0, len(linhas)),
		UltimoID:   desdeID,
	}
	for _, l := range linhas {
		resp.Mensagens = append(resp.Mensagens, mensagemConversaResponse{
			ID:           l.ID,
			Direcao:      l.Direcao,
			Tipo:         l.Tipo,
			Texto:        l.Texto,
			MidiaCaminho: l.MidiaCaminho,
			Status:       l.Status,
			CriadoEm:     formatarTimestamp(l.CriadoEm),
		})
		resp.UltimoID = l.ID
	}

	responderJSON(w, resp)
}

type marcarLidaRequest struct {
	AteID *int64 `json:"ate_id"`
}

// MarcarLida atende POST /v1/conversas/{id}/lida (G6). Corpo opcional:
// sem ate_id marca ate a ultima mensagem da conversa. A leitura nunca
// retrocede (GREATEST na query).
func (h *ConversasV1) MarcarLida(w http.ResponseWriter, r *http.Request) {
	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		http.Error(w, "nao autorizado", http.StatusUnauthorized)
		return
	}

	conversaID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || conversaID <= 0 {
		http.Error(w, "id de conversa invalido", http.StatusBadRequest)
		return
	}

	var req marcarLidaRequest
	corpo, err := io.ReadAll(io.LimitReader(r.Body, tamanhoMaximoPayload))
	if err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	if len(bytes.TrimSpace(corpo)) > 0 {
		if err := json.Unmarshal(corpo, &req); err != nil {
			http.Error(w, "payload invalido", http.StatusBadRequest)
			return
		}
	}
	if req.AteID != nil && *req.AteID <= 0 {
		http.Error(w, "ate_id deve ser positivo", http.StatusBadRequest)
		return
	}

	queries := store.New(h.pool)
	if _, err := queries.BuscarConversaComContatoPorID(r.Context(), conversaID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "conversa nao encontrada", http.StatusNotFound)
			return
		}
		slog.Error("conversas v1: buscar conversa", "conversa_id", conversaID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// 0 linhas = conversa sem mensagem e sem ate_id: nada a marcar, e o
	// resultado para quem chamou e o mesmo (nada nao lido).
	if _, err := queries.MarcarConversaLida(r.Context(), store.MarcarConversaLidaParams{
		AplicacaoID: app.ID,
		ConversaID:  conversaID,
		AteID:       req.AteID,
	}); err != nil {
		slog.Error("conversas v1: marcar lida", "conversa_id", conversaID, "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type abrirConversaRequest struct {
	Caixa        string `json:"caixa"`
	TelefoneE164 string `json:"telefone_e164"`
	Nome         string `json:"nome"`
}

type abrirConversaResponse struct {
	ID      int64           `json:"id"`
	Caixa   string          `json:"caixa"`
	Criada  bool            `json:"criada"`
	Contato contatoResponse `json:"contato"`
}

// Abrir atende POST /v1/conversas (G4): conversa com numero que nunca falou
// com a caixa. Idempotente por contato -- com conversa aberta, devolve ela
// (200, criada=false) em vez de criar outra.
//
// O @lid e resolvido aqui, como no disparo: e ele que faz a resposta do
// contato, quando chegar pelo webhook, cair nesta conversa mesmo se a z-api
// ocultar o telefone. Falha na resolucao nao impede a abertura; so a
// resposta "nao esta no WhatsApp" impede (422).
func (h *ConversasV1) Abrir(w http.ResponseWriter, r *http.Request) {
	var req abrirConversaRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	cx, ok := caixaDaRequisicao(w, r, h.caixas, req.Caixa)
	if !ok {
		return
	}
	telefone, err := identidade.NormalizarE164(req.TelefoneE164)
	if err != nil {
		http.Error(w, "telefone_e164 invalido", http.StatusBadRequest)
		return
	}
	nome := strings.TrimSpace(req.Nome)

	ctx := r.Context()

	var lid string
	var resolvedor resolvedorLid
	if h.identidade != nil {
		resolvedor = h.identidade(cx)
	}
	var resultado *identidade.ResultadoLid
	if resolvedor == nil {
		err = errors.New("caixa sem resolvedor de @lid")
	} else {
		resultado, err = resolvedor.ResolverLid(ctx, strings.TrimPrefix(telefone, "+"))
	}
	switch {
	case err != nil:
		slog.Warn("conversas v1: falha ao resolver lid, abrindo sem chat_lid", "erro", err)
	case !resultado.Resolvido:
		slog.Warn("conversas v1: z-api nao resolveu o telefone, abrindo sem chat_lid")
	case !resultado.Existe:
		http.Error(w, "numero nao esta no WhatsApp", http.StatusUnprocessableEntity)
		return
	default:
		lid = resultado.Lid
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("conversas v1: abrir: iniciar transacao", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	queries := store.New(tx)

	if err := queries.TravarIdentidadeDoContato(ctx, telefone); err != nil {
		slog.Error("conversas v1: abrir: travar identidade", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	lead, err := h.leadDoContato(ctx, queries, telefone, lid, nome)
	if err != nil {
		slog.Error("conversas v1: abrir: resolver lead", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	criada := false
	conversa, err := queries.BuscarConversaAbertaPorLead(ctx, store.BuscarConversaAbertaPorLeadParams{LeadID: lead.ID, CaixaID: cx.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		conversa, err = queries.CriarConversa(ctx, store.CriarConversaParams{LeadID: lead.ID, CaixaID: cx.ID})
		criada = true
	}
	if err != nil {
		slog.Error("conversas v1: abrir: obter conversa", "lead_id", lead.ID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	contato, err := queries.BuscarConversaComContatoPorID(ctx, conversa.ID)
	if err != nil {
		slog.Error("conversas v1: abrir: ler contato", "conversa_id", conversa.ID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("conversas v1: abrir: commit", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if criada {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(abrirConversaResponse{
		ID:     conversa.ID,
		Caixa:  cx.Codigo,
		Criada: criada,
		Contato: contatoResponse{
			Nome:         contato.ContatoNome,
			TelefoneE164: contato.ContatoTelefone,
			ChatLid:      contato.ContatoChatLid,
		},
	})
}

// leadDoContato acha o lead pela mesma precedencia do matcher (@lid, depois
// telefone) ou cria um. Lead achado por telefone adota o @lid resolvido.
func (h *ConversasV1) leadDoContato(ctx context.Context, queries *store.Queries, telefone, lid, nome string) (store.Lead, error) {
	if lid != "" {
		lead, err := queries.BuscarLeadPorChatLid(ctx, &lid)
		if err == nil {
			return lead, h.completarNome(ctx, queries, lead, nome)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return store.Lead{}, err
		}
	}

	lead, err := queries.BuscarLeadPorTelefone(ctx, &telefone)
	if err == nil {
		if lid != "" && lead.ChatLid == nil {
			if err := queries.PreencherChatLidSeVazio(ctx, store.PreencherChatLidSeVazioParams{ID: lead.ID, ChatLid: &lid}); err != nil {
				return store.Lead{}, err
			}
		}
		return lead, h.completarNome(ctx, queries, lead, nome)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.Lead{}, err
	}

	return queries.CriarLead(ctx, store.CriarLeadParams{
		Nome:         naoVazio(nome),
		TelefoneE164: &telefone,
		ChatLid:      naoVazio(lid),
		Origem:       naoVazio("aplicacao"),
	})
}

func (h *ConversasV1) completarNome(ctx context.Context, queries *store.Queries, lead store.Lead, nome string) error {
	if nome == "" || (lead.Nome != nil && *lead.Nome != "") {
		return nil
	}
	return queries.PreencherNomeDoLeadSeVazio(ctx, store.PreencherNomeDoLeadSeVazioParams{ID: lead.ID, Nome: &nome})
}

func formatarTimestamp(t pgtype.Timestamp) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format(layoutTempoResposta)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// previa trunca o texto para a lista de conversas (o portal nao precisa
// do texto inteiro para montar a tela) e vem vazia para midia -- o
// `tipo` ja diz o que e (secao G2/G3 do plano).
func previa(tipo string, texto *string) string {
	if tipo != "texto" || texto == nil {
		return ""
	}
	t := *texto
	if len([]rune(t)) <= tamanhoPreviaMaximo {
		return t
	}
	runas := []rune(t)
	return string(runas[:tamanhoPreviaMaximo]) + "…"
}
