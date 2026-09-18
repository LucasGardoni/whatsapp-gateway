package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
)

// SessoesSSE emite o token que autentica a conexao EventSource.
//
// Duas rotas, um so emissor (barramento, fase 3):
//
//   - POST /v1/sessoes      -- destino OPACO, aplicacao vem do token de servico
//   - POST /api/sessoes-sse -- corretor_id, traduzido para "crm:<id>" (ponte
//     de compatibilidade, some na fase 8)
//
// O gateway NAO verifica se quem pediu tem direito ao destino. Essa
// decisao e da aplicacao e acontece antes desta chamada -- a permissao e
// consequencia de quem emitiu o token (secao 1 do plano). Se este handler
// um dia consultar uma tabela para decidir quem pode ler o que, o modelo
// foi quebrado.
type SessoesSSE struct {
	// assinador nil = SSE_SIGNING_KEY ausente. Responde 503 em vez de
	// emitir token assinado com segredo vazio, que qualquer um forjaria.
	assinador *sse.AssinadorSessao
}

func NovoSessoesSSE(assinador *sse.AssinadorSessao) *SessoesSSE {
	return &SessoesSSE{assinador: assinador}
}

type criarSessaoSSERequest struct {
	// CorretorID e o campo legado de /api/sessoes-sse.
	CorretorID int64 `json:"corretor_id"`
	// Destino e o campo opaco de /v1/sessoes.
	Destino string `json:"destino"`
}

type criarSessaoSSEResponse struct {
	Token    string    `json:"token"`
	ExpiraEm time.Time `json:"expira_em"`
}

// Criar atende /v1/sessoes: destino opaco, aplicacao derivada do token.
func (h *SessoesSSE) Criar(w http.ResponseWriter, r *http.Request) {
	req, ok := h.lerRequisicao(w, r)
	if !ok {
		return
	}

	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		// o caminho legado (token de servico unico) nao identifica
		// aplicacao, e sem aplicacao nao ha como compor a chave do hub sem
		// inventar uma -- que e justamente o que separa um consumidor do
		// outro. Melhor recusar do que adivinhar.
		http.Error(w, "use um token de aplicacao para /v1/sessoes", http.StatusForbidden)
		return
	}

	destino := strings.TrimSpace(req.Destino)
	if destino == "" {
		http.Error(w, "destino e obrigatorio", http.StatusBadRequest)
		return
	}

	h.emitir(w, app.Codigo, destino)
}

// CriarLegado atende /api/sessoes-sse: recebe corretor_id e traduz para o
// destino "crm:<id>". Ponte de compatibilidade -- some na fase 8, quando o
// CRM passar a pedir sessao como qualquer outra aplicacao.
func (h *SessoesSSE) CriarLegado(w http.ResponseWriter, r *http.Request) {
	req, ok := h.lerRequisicao(w, r)
	if !ok {
		return
	}
	if req.CorretorID == 0 {
		http.Error(w, "corretor_id e obrigatorio", http.StatusBadRequest)
		return
	}

	h.emitir(w, sse.AplicacaoCRM, strconv.FormatInt(req.CorretorID, 10))
}

func (h *SessoesSSE) lerRequisicao(w http.ResponseWriter, r *http.Request) (criarSessaoSSERequest, bool) {
	if h.assinador == nil {
		http.Error(w, "tempo real nao configurado", http.StatusServiceUnavailable)
		return criarSessaoSSERequest{}, false
	}

	var req criarSessaoSSERequest
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return criarSessaoSSERequest{}, false
	}
	return req, true
}

func (h *SessoesSSE) emitir(w http.ResponseWriter, app, destino string) {
	token, expiraEm, err := h.assinador.Emitir(app, destino)
	if err != nil {
		// o destino NAO entra no log: e opaco para o gateway, mas nao
		// necessariamente inocuo para a aplicacao, e log de gateway e lido
		// por quem opera o gateway, nao por quem e dono do dado.
		slog.Error("sessoes sse: emitir token", "aplicacao", app, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(criarSessaoSSEResponse{Token: token, ExpiraEm: expiraEm})
}
