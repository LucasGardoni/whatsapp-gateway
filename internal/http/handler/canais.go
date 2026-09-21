package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Canais gerencia canal e lista de entrega (barramento, fase 4).
//
// As quatro rotas sao idempotentes: a aplicacao reconcilia o estado dela
// contra o gateway sem precisar saber o que ja mandou antes. PUT repetido
// confirma, DELETE do que nao existe tambem responde sucesso.
//
// Nada aqui interpreta canal_externo ou destino: sao strings opacas cujo
// dono e a aplicacao (secao 1 do plano). O gateway nao decide quem entra
// na lista -- so guarda a lista que a aplicacao registrou. Se um dia
// aparecer aqui uma regra sobre QUEM pode assinar o que, houve vazamento;
// volte a secao 2.
type Canais struct {
	pool *pgxpool.Pool
}

func NovoCanais(pool *pgxpool.Pool) *Canais {
	return &Canais{pool: pool}
}

// tamanhoMaximoIdentificador limita canal_externo e destino_externo.
//
// O gateway nao interpreta esses valores, mas eles viram chave de indice e
// prefixo de chave do hub -- sem teto, uma aplicacao com um bug de
// concatenacao enche a tabela com strings de megabytes. O limite e
// generoso: identificador legitimo nao chega perto.
const tamanhoMaximoIdentificador = 256

type assinanteResponse struct {
	Destino string `json:"destino"`
}

type listarAssinantesResponse struct {
	Canal      string              `json:"canal_externo"`
	Assinantes []assinanteResponse `json:"assinantes"`
}

type canalResponse struct {
	ID    int64  `json:"id"`
	Canal string `json:"canal_externo"`
}

// PutCanal cria ou confirma o canal. Idempotente.
func (h *Canais) PutCanal(w http.ResponseWriter, r *http.Request) {
	app, canalExterno, ok := h.escopo(w, r)
	if !ok {
		return
	}

	canal, err := store.New(h.pool).UpsertCanal(r.Context(), store.UpsertCanalParams{
		AplicacaoID:  app.ID,
		CanalExterno: canalExterno,
	})
	if err != nil {
		slog.Error("canais: upsert de canal", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	responderJSON(w, canalResponse{ID: canal.ID, Canal: canal.CanalExterno})
}

// PutAssinante adiciona um destino a lista de entrega. Idempotente.
func (h *Canais) PutAssinante(w http.ResponseWriter, r *http.Request) {
	app, canalExterno, ok := h.escopo(w, r)
	if !ok {
		return
	}
	destino, ok := h.destino(w, r)
	if !ok {
		return
	}

	// o upsert casa o canal por (aplicacao, canal_externo) dentro do
	// proprio INSERT: se o canal nao existe para ESTA aplicacao, nenhuma
	// linha entra. Zero linhas afetadas e indistinguivel de "ja estava la",
	// entao o 404 precisa de uma checagem propria -- feita antes, para a
	// aplicacao saber que errou o canal em vez de achar que assinou.
	queries := store.New(h.pool)
	if _, err := queries.BuscarCanal(r.Context(), store.BuscarCanalParams{
		AplicacaoID:  app.ID,
		CanalExterno: canalExterno,
	}); errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "canal nao encontrado", http.StatusNotFound)
		return
	} else if err != nil {
		slog.Error("canais: buscar canal", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	if err := queries.UpsertAssinante(r.Context(), store.UpsertAssinanteParams{
		AplicacaoID:    app.ID,
		CanalExterno:   canalExterno,
		DestinoExterno: destino,
	}); err != nil {
		slog.Error("canais: upsert de assinante", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteAssinante remove um destino da lista. Idempotente: remover quem
// nao esta la tambem responde 204 -- o estado final e o mesmo, e e o
// estado final que a aplicacao esta tentando garantir.
func (h *Canais) DeleteAssinante(w http.ResponseWriter, r *http.Request) {
	app, canalExterno, ok := h.escopo(w, r)
	if !ok {
		return
	}
	destino, ok := h.destino(w, r)
	if !ok {
		return
	}

	linhas, err := store.New(h.pool).RemoverAssinante(r.Context(), store.RemoverAssinanteParams{
		AplicacaoID:    app.ID,
		CanalExterno:   canalExterno,
		DestinoExterno: destino,
	})
	if err != nil {
		slog.Error("canais: remover assinante", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// remocao de acesso e o evento que mais importa numa investigacao
	// depois -- vale a linha de log. O destino entra aqui porque e a
	// propria aplicacao pedindo, e sem ele o log nao serve para nada.
	if linhas > 0 {
		slog.Info("canais: assinante removido", "aplicacao", app.Codigo, "canal", canalExterno, "destino", destino)
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetAssinantes lista os destinos do canal.
func (h *Canais) GetAssinantes(w http.ResponseWriter, r *http.Request) {
	app, canalExterno, ok := h.escopo(w, r)
	if !ok {
		return
	}

	linhas, err := store.New(h.pool).ListarAssinantes(r.Context(), store.ListarAssinantesParams{
		AplicacaoID:  app.ID,
		CanalExterno: canalExterno,
	})
	if err != nil {
		slog.Error("canais: listar assinantes", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// lista vazia sai como [] e nunca como null: quem consome em JS faria
	// .map em null e quebraria a tela por causa de um canal sem assinante,
	// que e um estado perfeitamente normal.
	assinantes := make([]assinanteResponse, 0, len(linhas))
	for _, l := range linhas {
		assinantes = append(assinantes, assinanteResponse{Destino: l.DestinoExterno})
	}

	responderJSON(w, listarAssinantesResponse{Canal: canalExterno, Assinantes: assinantes})
}

// escopo devolve a aplicacao autenticada e o canal do path. E o unico
// ponto de entrada do escopo: nenhum handler daqui monta a consulta sem
// passar por ele.
func (h *Canais) escopo(w http.ResponseWriter, r *http.Request) (middleware.Aplicacao, string, bool) {
	return escopoCanal(w, r)
}

// escopoCanal e o escopo de toda rota sob /v1/canais/{canal_externo},
// inclusive as que nao vivem neste arquivo (o historico da fase 5).
// Compartilhado de proposito: duas leituras diferentes do mesmo par
// (aplicacao, canal) e como uma fronteira entre consumidores vaza pelo
// lado que ficou para tras.
func escopoCanal(w http.ResponseWriter, r *http.Request) (middleware.Aplicacao, string, bool) {
	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		// defesa em profundidade: a rota ja esta atras do middleware de
		// aplicacao. Sem aplicacao nao existe escopo, e sem escopo uma
		// consulta aqui atravessaria a fronteira entre consumidores.
		http.Error(w, "nao autorizado", http.StatusUnauthorized)
		return middleware.Aplicacao{}, "", false
	}

	canal, ok := parametroOpaco(w, r, "canal_externo")
	if !ok {
		return middleware.Aplicacao{}, "", false
	}
	return app, canal, true
}

func (h *Canais) destino(w http.ResponseWriter, r *http.Request) (string, bool) {
	return parametroOpaco(w, r, "destino")
}

// parametroOpaco le um segmento de path que o gateway nao interpreta --
// so valida presenca e tamanho, nunca formato (secao 4: "o gateway valida
// apenas tamanho e presenca").
func parametroOpaco(w http.ResponseWriter, r *http.Request, nome string) (string, bool) {
	valor := strings.TrimSpace(chi.URLParam(r, nome))
	if valor == "" {
		http.Error(w, nome+" e obrigatorio", http.StatusBadRequest)
		return "", false
	}
	if len(valor) > tamanhoMaximoIdentificador {
		http.Error(w, nome+" excede o tamanho maximo", http.StatusBadRequest)
		return "", false
	}
	return valor, true
}

func responderJSON(w http.ResponseWriter, corpo any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(corpo)
}
