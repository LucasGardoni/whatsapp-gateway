package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
)

// SessoesSSE emite o token curto que autentica a conexao EventSource do
// CRM (secao 4.4 do plano do CRM) -- protegido pelo mesmo token de
// servico dos outros endpoints internos (GATEWAY_SERVICE_TOKEN), nunca
// exposto ao browser.
type SessoesSSE struct {
	tokens *sse.TokenStore
}

func NovoSessoesSSE(tokens *sse.TokenStore) *SessoesSSE {
	return &SessoesSSE{tokens: tokens}
}

type criarSessaoSSERequest struct {
	CorretorID int64 `json:"corretor_id"`
}

type criarSessaoSSEResponse struct {
	Token    string    `json:"token"`
	ExpiraEm time.Time `json:"expira_em"`
}

func (h *SessoesSSE) Criar(w http.ResponseWriter, r *http.Request) {
	var req criarSessaoSSERequest
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	if req.CorretorID == 0 {
		http.Error(w, "corretor_id e obrigatorio", http.StatusBadRequest)
		return
	}

	token, expiraEm, err := h.tokens.Emitir(req.CorretorID)
	if err != nil {
		slog.Error("sessoes sse: emitir token", "corretor_id", req.CorretorID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(criarSessaoSSEResponse{Token: token, ExpiraEm: expiraEm})
}
