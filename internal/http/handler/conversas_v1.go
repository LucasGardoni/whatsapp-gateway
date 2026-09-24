package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// ConversasV1 atende G2/G3 do barramento (docs/PLANO_MULTICAIXA_E_CONVERSAS.md):
// GET /v1/conversas e GET /v1/conversas/{id}/mensagens. Devolver o que o
// gateway mesmo gravou em `conversa`/`mensagem` nao e consulta de dominio
// da aplicacao -- e por isso vive aqui, sem nenhum if por aplicacao.
//
// nao_lidas fica em 0 por enquanto: a contagem depende da marcacao de
// leitura por aplicacao (G6, ainda nao construido -- ver "ordem de
// execucao" do plano). O campo ja existe no contrato para o portal nao
// precisar mudar o shape da resposta quando G6 chegar.
type ConversasV1 struct {
	pool *pgxpool.Pool
	// caixaCodigo e o codigo constante da unica caixa que existe hoje
	// (G1 -- multi-caixa -- ainda nao foi construido). Enquanto so houver
	// uma, o parametro `caixa` da query e validado contra ela em vez de
	// ignorado: um valor errado e sinal de bug no chamador, nao motivo
	// pra silenciosamente devolver a caixa certa.
	caixaCodigo string
}

func NovoConversasV1(pool *pgxpool.Pool, caixaCodigo string) *ConversasV1 {
	return &ConversasV1{pool: pool, caixaCodigo: caixaCodigo}
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

	if caixa := strings.TrimSpace(q.Get("caixa")); caixa != "" && caixa != h.caixaCodigo {
		http.Error(w, "caixa desconhecida: "+caixa, http.StatusBadRequest)
		return
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

	linhas, err := store.New(h.pool).ListarConversas(r.Context(), store.ListarConversasParams{
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
			Caixa: h.caixaCodigo,
			Contato: contatoResponse{
				Nome:         l.ContatoNome,
				TelefoneE164: l.ContatoTelefone,
				ChatLid:      l.ContatoChatLid,
			},
			AbertaEm: formatarTimestamp(l.AbertaEm),
			// nao_lidas: ver comentario do tipo ConversasV1 -- depende de G6.
			NaoLidas: 0,
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
