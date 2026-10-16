package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// GruposV1 atende os grupos de WhatsApp da caixa (migration 00031): a
// reconciliacao com o get-groups, entrar por link, sair e o metadata. Quem
// pode ver cada grupo e decisao da aplicacao, nao do gateway.
type GruposV1 struct {
	pool   *pgxpool.Pool
	caixas caixasV1
	// provedor devolve a instancia DA CAIXA. Nil = caixa sem z-api (fake).
	provedor func(caixa.Caixa) provedorGrupos
}

type provedorGrupos interface {
	Grupos(ctx context.Context, page, pageSize int) ([]zapi.Grupo, error)
	MetadataGrupo(ctx context.Context, grupoID string) (*zapi.MetadataGrupo, error)
	AceitarConvite(ctx context.Context, link string) error
	SairDoGrupo(ctx context.Context, grupoID string) error
}

const (
	pageSizeGrupos   = 100
	maxPaginasGrupos = 100
)

func NovoGruposV1(pool *pgxpool.Pool, caixas *caixa.Registro) *GruposV1 {
	return &GruposV1{
		pool:   pool,
		caixas: caixas,
		provedor: func(c caixa.Caixa) provedorGrupos {
			if cl, ok := caixas.ZAPI(c); ok {
				return cl
			}
			return nil
		},
	}
}

type sincronizarGruposRequest struct {
	Caixa string `json:"caixa"`
}

type sincronizarGruposResponse struct {
	Caixa     string `json:"caixa"`
	NoNumero  int    `json:"no_numero"`
	Criados   int    `json:"criados"`
	Reabertos int    `json:"reabertos"`
	Fechados  int    `json:"fechados"`
	// FechamentoSuspenso: a z-api devolveu zero grupos com grupos abertos no
	// banco. Lista vazia por falha da instancia fecharia tudo, entao nada fecha.
	FechamentoSuspenso bool `json:"fechamento_suspenso"`
}

// Sincronizar atende POST /v1/grupos/sincronizar: grupo novo no numero ganha
// conversa, grupo que voltou reabre, grupo que sumiu fecha. E a rede de
// seguranca dos eventos de entrada/saida do webhook.
func (h *GruposV1) Sincronizar(w http.ResponseWriter, r *http.Request) {
	var req sincronizarGruposRequest
	if !lerJSON(w, r, &req) {
		return
	}
	cx, ok := caixaDaRequisicao(w, r, h.caixas, req.Caixa)
	if !ok {
		return
	}
	p := h.provedor(cx)
	if p == nil {
		http.Error(w, "caixa sem provedor de grupos", http.StatusConflict)
		return
	}
	h.sincronizarCaixa(w, r, cx, p)
}

func (h *GruposV1) sincronizarCaixa(w http.ResponseWriter, r *http.Request, cx caixa.Caixa, p provedorGrupos) {
	noNumero := map[string]string{}
	for page := 1; page <= maxPaginasGrupos; page++ {
		grupos, err := p.Grupos(r.Context(), page, pageSizeGrupos)
		if err != nil {
			slog.Error("grupos v1: listar na z-api", "caixa", cx.Codigo, "page", page, "erro", err)
			http.Error(w, "erro ao consultar grupos no provedor", http.StatusBadGateway)
			return
		}
		for _, g := range grupos {
			if id := strings.TrimSpace(g.Phone); id != "" {
				noNumero[id] = strings.TrimSpace(g.Name)
			}
		}
		if len(grupos) < pageSizeGrupos {
			break
		}
	}

	resp, err := h.reconciliar(r.Context(), cx, noNumero)
	if err != nil {
		slog.Error("grupos v1: reconciliar", "caixa", cx.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	responderJSON(w, resp)
}

func (h *GruposV1) reconciliar(ctx context.Context, cx caixa.Caixa, noNumero map[string]string) (sincronizarGruposResponse, error) {
	resp := sincronizarGruposResponse{Caixa: cx.Codigo, NoNumero: len(noNumero)}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return resp, err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)

	conhecidos, err := q.ListarGruposDaCaixa(ctx, cx.ID)
	if err != nil {
		return resp, err
	}
	abertos := 0
	for _, g := range conhecidos {
		if !g.FechadaEm.Valid {
			abertos++
		}
	}
	resp.FechamentoSuspenso = len(noNumero) == 0 && abertos > 0

	vistos := map[string]bool{}
	for _, g := range conhecidos {
		id := deref(g.GrupoID)
		vistos[id] = true
		_, esta := noNumero[id]
		switch {
		case esta && g.FechadaEm.Valid:
			if err := q.ReabrirConversa(ctx, g.ConversaID); err != nil {
				return resp, err
			}
			resp.Reabertos++
		case !esta && !g.FechadaEm.Valid && !resp.FechamentoSuspenso:
			n, err := q.FecharConversa(ctx, g.ConversaID)
			if err != nil {
				return resp, err
			}
			resp.Fechados += int(n)
		}
		if esta {
			if nome := noNumero[id]; nome != "" {
				if err := q.AtualizarNomeDoGrupo(ctx, store.AtualizarNomeDoGrupoParams{ID: g.LeadID, Nome: &nome}); err != nil {
					return resp, err
				}
			}
		}
	}

	for id, nome := range noNumero {
		if vistos[id] {
			continue
		}
		if err := q.TravarGrupo(ctx, id); err != nil {
			return resp, err
		}
		leadID, err := leadDoGrupo(ctx, q, id, nome)
		if err != nil {
			return resp, err
		}
		if _, criada, err := conversaDoGrupo(ctx, q, leadID, cx.ID, true); err != nil {
			return resp, err
		} else if criada {
			resp.Criados++
		}
	}

	return resp, tx.Commit(ctx)
}

type entrarGrupoRequest struct {
	Caixa string `json:"caixa"`
	Link  string `json:"link"`
}

// Entrar atende POST /v1/grupos/entrar: aceita o convite e reconcilia, para a
// conversa ja existir na resposta (o webhook de entrada pode demorar).
func (h *GruposV1) Entrar(w http.ResponseWriter, r *http.Request) {
	var req entrarGrupoRequest
	if !lerJSON(w, r, &req) {
		return
	}
	link := strings.TrimSpace(req.Link)
	if !strings.HasPrefix(link, "https://chat.whatsapp.com/") {
		http.Error(w, "link deve ser um convite https://chat.whatsapp.com/...", http.StatusBadRequest)
		return
	}
	cx, ok := caixaDaRequisicao(w, r, h.caixas, req.Caixa)
	if !ok {
		return
	}
	p := h.provedor(cx)
	if p == nil {
		http.Error(w, "caixa sem provedor de grupos", http.StatusConflict)
		return
	}
	if err := p.AceitarConvite(r.Context(), link); err != nil {
		slog.Warn("grupos v1: aceitar convite", "caixa", cx.Codigo, "erro", err)
		http.Error(w, "a z-api recusou o convite (link invalido, expirado ou numero sem permissao)", http.StatusUnprocessableEntity)
		return
	}
	h.sincronizarCaixa(w, r, cx, p)
}

// Sair atende POST /v1/conversas/{id}/grupo/sair.
func (h *GruposV1) Sair(w http.ResponseWriter, r *http.Request) {
	g, cx, p, ok := h.grupoDaRota(w, r)
	if !ok {
		return
	}
	if err := p.SairDoGrupo(r.Context(), deref(g.GrupoID)); err != nil {
		slog.Error("grupos v1: sair do grupo", "caixa", cx.Codigo, "conversa_id", g.ID, "erro", err)
		http.Error(w, "erro ao sair do grupo no provedor", http.StatusBadGateway)
		return
	}
	if _, err := store.New(h.pool).FecharConversa(r.Context(), g.ID); err != nil {
		slog.Error("grupos v1: fechar conversa", "conversa_id", g.ID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type participanteGrupoResponse struct {
	Telefone   string  `json:"telefone"`
	Nome       *string `json:"nome"`
	Admin      bool    `json:"admin"`
	SuperAdmin bool    `json:"super_admin"`
}

type metadataGrupoResponse struct {
	ConversaID       int64                       `json:"conversa_id"`
	GrupoID          string                      `json:"grupo_id"`
	Nome             string                      `json:"nome"`
	Descricao        string                      `json:"descricao"`
	Dono             string                      `json:"dono"`
	SoAdminEnvia     bool                        `json:"so_admin_envia"`
	SoAdminConfigura bool                        `json:"so_admin_configura"`
	ComunidadeID     *string                     `json:"comunidade_id"`
	Participantes    []participanteGrupoResponse `json:"participantes"`
}

// Metadata atende GET /v1/conversas/{id}/grupo.
func (h *GruposV1) Metadata(w http.ResponseWriter, r *http.Request) {
	g, cx, p, ok := h.grupoDaRota(w, r)
	if !ok {
		return
	}
	m, err := p.MetadataGrupo(r.Context(), deref(g.GrupoID))
	if err != nil {
		slog.Warn("grupos v1: metadata", "caixa", cx.Codigo, "conversa_id", g.ID, "erro", err)
		http.Error(w, "erro ao consultar o grupo no provedor", http.StatusBadGateway)
		return
	}
	resp := metadataGrupoResponse{
		ConversaID:       g.ID,
		GrupoID:          deref(g.GrupoID),
		Nome:             m.Subject,
		Descricao:        m.Description,
		Dono:             m.Owner,
		SoAdminEnvia:     m.AdminOnlyMessage,
		SoAdminConfigura: m.AdminOnlySettings,
		ComunidadeID:     m.CommunityID,
		Participantes:    make([]participanteGrupoResponse, 0, len(m.Participants)),
	}
	for _, pt := range m.Participants {
		nome := strings.TrimSpace(pt.Name)
		if nome == "" {
			nome = strings.TrimSpace(pt.Short)
		}
		resp.Participantes = append(resp.Participantes, participanteGrupoResponse{
			Telefone:   pt.Phone,
			Nome:       naoVazio(nome),
			Admin:      pt.IsAdmin || pt.IsSuperAdmin,
			SuperAdmin: pt.IsSuperAdmin,
		})
	}
	responderJSON(w, resp)
}

func (h *GruposV1) grupoDaRota(w http.ResponseWriter, r *http.Request) (store.BuscarGrupoDaConversaRow, caixa.Caixa, provedorGrupos, bool) {
	var vazio store.BuscarGrupoDaConversaRow
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "id de conversa invalido", http.StatusBadRequest)
		return vazio, caixa.Caixa{}, nil, false
	}
	g, err := store.New(h.pool).BuscarGrupoDaConversa(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversa de grupo nao encontrada", http.StatusNotFound)
		return vazio, caixa.Caixa{}, nil, false
	}
	if err != nil {
		slog.Error("grupos v1: buscar grupo", "conversa_id", id, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return vazio, caixa.Caixa{}, nil, false
	}
	cx, err := h.caixaPorID(r.Context(), g.CaixaID)
	if err != nil {
		http.Error(w, "caixa da conversa inativa", http.StatusConflict)
		return vazio, caixa.Caixa{}, nil, false
	}
	p := h.provedor(cx)
	if p == nil {
		http.Error(w, "caixa sem provedor de grupos", http.StatusConflict)
		return vazio, caixa.Caixa{}, nil, false
	}
	return g, cx, p, true
}

// caixaPorID procura entre as ativas: grupo de caixa desligada nao opera.
func (h *GruposV1) caixaPorID(ctx context.Context, id int64) (caixa.Caixa, error) {
	ativas, ok := h.caixas.(interface {
		Ativas(ctx context.Context) ([]caixa.Caixa, error)
	})
	if !ok {
		return caixa.Caixa{}, caixa.ErrDesconhecida
	}
	lista, err := ativas.Ativas(ctx)
	if err != nil {
		return caixa.Caixa{}, err
	}
	for _, c := range lista {
		if c.ID == id {
			return c, nil
		}
	}
	return caixa.Caixa{}, caixa.ErrDesconhecida
}

func lerJSON(w http.ResponseWriter, r *http.Request, destino any) bool {
	corpo, err := io.ReadAll(io.LimitReader(r.Body, tamanhoMaximoPayload))
	if err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(corpo, destino); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return false
	}
	return true
}
