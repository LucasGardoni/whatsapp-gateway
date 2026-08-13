package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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

	ctx := r.Context()
	queries := store.New(h.pool)

	disparo, err := h.criarParaLead(ctx, queries, req.LeadID, req.Telefone, req.Template, req.NomeEmpreendimento)
	if err != nil {
		var erroHTTP *erroDisparoHTTP
		if errors.As(err, &erroHTTP) {
			if erroHTTP.status >= http.StatusInternalServerError {
				slog.Error("disparo: criar", "lead_id", req.LeadID, "erro", err)
			}
			http.Error(w, erroHTTP.mensagem, erroHTTP.status)
			return
		}
		slog.Error("disparo: criar", "lead_id", req.LeadID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(criarDisparoResponse{
		Token: disparo.Token,
		Link:  h.baseURLPagina + "/c/" + disparo.Token,
	})
}

// erroDisparoHTTP carrega o status HTTP certo pra criarParaLead poder ser
// reusado tanto por Criar (responde direto ao chamador) quanto por
// Reenviar (so contabiliza falha e segue pro proximo candidato).
type erroDisparoHTTP struct {
	status   int
	mensagem string
}

func (e *erroDisparoHTTP) Error() string { return e.mensagem }

// criarParaLead resolve o @lid, cria o token e o registro de disparo --
// nucleo comum entre Criar (chamado pelo CRM por lead) e Reenviar (job em
// lote da fase 11, "quem nao engajou").
func (h *Disparo) criarParaLead(ctx context.Context, queries *store.Queries, leadID int64, telefone, template, nomeEmpreendimento string) (store.Disparo, error) {
	telefoneNormalizado, err := identidade.NormalizarE164(telefone)
	if err != nil {
		return store.Disparo{}, &erroDisparoHTTP{status: http.StatusBadRequest, mensagem: fmt.Sprintf("telefone invalido: %v", err)}
	}

	resultadoLid, err := h.identidade.ResolverLid(ctx, strings.TrimPrefix(telefoneNormalizado, "+"))
	if err != nil {
		return store.Disparo{}, fmt.Errorf("resolver lid do lead %d: %w", leadID, &erroDisparoHTTP{status: http.StatusBadGateway, mensagem: "erro ao resolver identidade do telefone"})
	}
	if resultadoLid.Existe && resultadoLid.Lid != "" {
		if err := queries.AtualizarChatLidDoLead(ctx, store.AtualizarChatLidDoLeadParams{
			ID:      leadID,
			ChatLid: naoVazio(resultadoLid.Lid),
		}); err != nil {
			return store.Disparo{}, fmt.Errorf("atualizar chat_lid do lead %d: %w", leadID, err)
		}
	}

	token, err := gerarToken()
	if err != nil {
		return store.Disparo{}, fmt.Errorf("gerar token: %w", err)
	}

	disparo, err := queries.CriarDisparo(ctx, store.CriarDisparoParams{
		LeadID:             leadID,
		Template:           template,
		Token:              token,
		NomeEmpreendimento: naoVazio(nomeEmpreendimento),
	})
	if err != nil {
		return store.Disparo{}, fmt.Errorf("criar disparo para lead %d: %w", leadID, err)
	}
	return disparo, nil
}

// parametroReenvioJanelaHoras / parametroReenvioMaxTentativas moram na
// tabela parametro (secao 7) -- ajustaveis sem redeploy, com defaults
// sensatos quando ausentes.
const (
	parametroReenvioJanelaHoras   = "reenvio_janela_horas"
	parametroReenvioMaxTentativas = "reenvio_max_tentativas"

	reenvioJanelaHorasPadrao   = 48
	reenvioMaxTentativasPadrao = 3
)

type reenviarRequest struct {
	// Template e NomeEmpreendimento sao opcionais -- sem eles, o job reusa
	// o do ultimo disparo de cada lead (mesma campanha, so tenta de novo).
	Template           string `json:"template"`
	NomeEmpreendimento string `json:"nome_empreendimento"`
}

type reenviarResponse struct {
	Candidatos int      `json:"candidatos"`
	Reenviados int      `json:"reenviados"`
	Falhas     []string `json:"falhas,omitempty"`
}

// Reenviar e o job de reenvio da fase 11 -- "quem nao engajou". Dono e o
// supervisor: a tela que aciona isso e 100% CRM, aqui so tem a parte que
// precisa saber falar com o whatsapp (secao 5). Um lead falhando nao para
// os outros -- o job segue e reporta as falhas no resumo.
func (h *Disparo) Reenviar(w http.ResponseWriter, r *http.Request) {
	var req reenviarRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
			http.Error(w, "payload invalido", http.StatusBadRequest)
			return
		}
	}

	ctx := r.Context()
	queries := store.New(h.pool)

	janela := tempoParametroHoras(ctx, queries, parametroReenvioJanelaHoras, reenvioJanelaHorasPadrao)
	maxTentativas := intParametro(ctx, queries, parametroReenvioMaxTentativas, reenvioMaxTentativasPadrao)

	// a janela e aplicada dentro da query, no relogio do banco (P1-08): o
	// corte era feito aqui com time.Now(), contra um enviado_em gravado com
	// LOCALTIMESTAMP. Com 3h de diferenca, ou o reenvio disparava cedo (e o
	// cliente recebia mensagem repetida pouco depois da primeira), ou
	// atrasava 3h. O teto de tentativas segue em Go, que nao envolve tempo.
	candidatos, err := queries.BuscarLeadsNaoEngajadosParaReenvio(ctx, janela.Seconds())
	if err != nil {
		slog.Error("disparo: reenvio: buscar candidatos", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	resp := reenviarResponse{}
	for _, c := range candidatos {
		if int(c.TotalDisparos) >= maxTentativas {
			continue // ja tentou o suficiente, nao insiste mais
		}
		if c.TelefoneE164 == nil {
			continue // BuscarLeadsNaoEngajadosParaReenvio ja filtra isso, defensivo
		}
		resp.Candidatos++

		template := req.Template
		if template == "" {
			template = c.Template
		}
		nomeEmpreendimento := req.NomeEmpreendimento
		if nomeEmpreendimento == "" && c.NomeEmpreendimento != nil {
			nomeEmpreendimento = *c.NomeEmpreendimento
		}

		if _, err := h.criarParaLead(ctx, queries, c.LeadID, *c.TelefoneE164, template, nomeEmpreendimento); err != nil {
			slog.Error("disparo: reenvio: falha em um lead, seguindo pros demais", "lead_id", c.LeadID, "erro", err)
			resp.Falhas = append(resp.Falhas, fmt.Sprintf("lead %d: %v", c.LeadID, err))
			continue
		}
		resp.Reenviados++
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// intParametro/tempoParametroHoras leem um inteiro configuravel da tabela
// parametro (secao 7), caindo no default quando ausente ou nao-numerico --
// nunca falha o request por causa de um parametro mal configurado.
func intParametro(ctx context.Context, queries *store.Queries, chave string, padrao int) int {
	p, err := queries.BuscarParametro(ctx, chave)
	if err != nil || p.Valor == nil {
		return padrao
	}
	v, err := strconv.Atoi(strings.TrimSpace(*p.Valor))
	if err != nil {
		return padrao
	}
	return v
}

func tempoParametroHoras(ctx context.Context, queries *store.Queries, chave string, padraoHoras int) time.Duration {
	return time.Duration(intParametro(ctx, queries, chave, padraoHoras)) * time.Hour
}

func gerarToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gerar token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
