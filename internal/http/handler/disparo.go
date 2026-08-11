package handler

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Disparo cria o registro que liga um envio de template (numero A) ao
// token clicavel de /c/{token}. Chamado pelo CRM, que decide quem recebe
// o lead e qual template disparar -- o gateway so cuida do que e
// WhatsApp (secao 5).
type Disparo struct {
	pool          *pgxpool.Pool
	identidade    *identidade.Cliente
	baseURLPagina string
}

func NovoDisparo(pool *pgxpool.Pool, identidadeCliente *identidade.Cliente, baseURLPagina string) *Disparo {
	return &Disparo{pool: pool, identidade: identidadeCliente, baseURLPagina: strings.TrimSuffix(baseURLPagina, "/")}
}

type criarDisparoRequest struct {
	LeadID             int64  `json:"lead_id"`
	Telefone           string `json:"telefone"`
	Template           string `json:"template"`
	NomeEmpreendimento string `json:"nome_empreendimento"`
}

type criarDisparoResponse struct {
	Token string `json:"token"`
	Link  string `json:"link"`
}

// Criar resolve o @lid do telefone antes de qualquer outra coisa (secao
// 4.3 -- e o unico momento em que temos o telefone com certeza) e grava no
// lead, cria o token e o registro de disparo.
func (h *Disparo) Criar(w http.ResponseWriter, r *http.Request) {
	var req criarDisparoRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}
	if req.LeadID == 0 || req.Telefone == "" || req.Template == "" {
		http.Error(w, "lead_id, telefone e template sao obrigatorios", http.StatusBadRequest)
		return
	}

	telefoneNormalizado, err := identidade.NormalizarE164(req.Telefone)
	if err != nil {
		http.Error(w, fmt.Sprintf("telefone invalido: %v", err), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	queries := store.New(h.pool)

	resultadoLid, err := h.identidade.ResolverLid(ctx, strings.TrimPrefix(telefoneNormalizado, "+"))
	if err != nil {
		slog.Error("disparo: resolver lid", "lead_id", req.LeadID, "erro", err)
		http.Error(w, "erro ao resolver identidade do telefone", http.StatusBadGateway)
		return
	}
	if resultadoLid.Existe && resultadoLid.Lid != "" {
		if err := queries.AtualizarChatLidDoLead(ctx, store.AtualizarChatLidDoLeadParams{
			ID:      req.LeadID,
			ChatLid: naoVazio(resultadoLid.Lid),
		}); err != nil {
			slog.Error("disparo: atualizar chat_lid do lead", "lead_id", req.LeadID, "erro", err)
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
	}

	token, err := gerarToken()
	if err != nil {
		slog.Error("disparo: gerar token", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	disparo, err := queries.CriarDisparo(ctx, store.CriarDisparoParams{
		LeadID:             req.LeadID,
		Template:           req.Template,
		Token:              token,
		NomeEmpreendimento: naoVazio(req.NomeEmpreendimento),
	})
	if err != nil {
		slog.Error("disparo: criar disparo", "lead_id", req.LeadID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(criarDisparoResponse{
		Token: disparo.Token,
		Link:  h.baseURLPagina + "/c/" + disparo.Token,
	})
}

func gerarToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gerar token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
