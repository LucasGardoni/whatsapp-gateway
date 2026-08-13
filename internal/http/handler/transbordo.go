package handler

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// chaveParametroNumeroB e a chave em `parametro` onde mora o numero B
// vigente -- trocar de numero e um UPDATE nessa linha (secao 7 do plano).
const chaveParametroNumeroB = "numero_b_vigente"

// Transbordo serve a pagina publica de /c/{token}. Nao e um 302 seco --
// precisa ser uma pagina real com o nome do empreendimento e um botao,
// porque o WhatsApp so abre com o cliente apertando enviar (secao 3).
type Transbordo struct {
	pool *pgxpool.Pool
}

func NovoTransbordo(pool *pgxpool.Pool) *Transbordo {
	return &Transbordo{pool: pool}
}

func (h *Transbordo) RedirecionarClique(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	queries := store.New(h.pool)

	disparo, err := queries.BuscarDisparoPorToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("transbordo: buscar disparo", "token", token, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// registra o clique antes de montar a pagina (secao 9 -- "registro do
	// clique antes do redirect"). Falha aqui nao impede a pagina de abrir:
	// o cliente ja clicou, e negar a ele o caminho pro WhatsApp por causa
	// de um problema nosso de gravacao seria perder o lead de vez. Por isso
	// tudo aqui e best-effort e so loga.
	h.registrarClique(r, token, disparo.LeadID)

	parametroNumeroB, err := queries.BuscarParametro(ctx, chaveParametroNumeroB)
	if err != nil || parametroNumeroB.Valor == nil || *parametroNumeroB.Valor == "" {
		slog.Error("transbordo: numero b vigente nao configurado no parametro", "erro", err)
		http.Error(w, "serviço indisponível, tente novamente em breve", http.StatusServiceUnavailable)
		return
	}

	nomeEmpreendimento := "nosso time"
	if disparo.NomeEmpreendimento != nil && *disparo.NomeEmpreendimento != "" {
		nomeEmpreendimento = *disparo.NomeEmpreendimento
	}

	texto := montarTextoTransbordo(nomeEmpreendimento, token)
	linkWhatsapp := fmt.Sprintf("https://wa.me/%s?text=%s", *parametroNumeroB.Valor, url.QueryEscape(texto))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := paginaTransbordo.Execute(w, dadosPaginaTransbordo{
		NomeEmpreendimento: nomeEmpreendimento,
		LinkWhatsapp:       linkWhatsapp,
	}); err != nil {
		slog.Error("transbordo: renderizar pagina", "erro", err)
	}
}

// registrarClique grava as tres consequencias de um clique -- o registro
// em `clique`, o `disparo.status` e o avanco do lead para 'clicou' (fase
// 4) -- numa transacao so, para nao existir estado meio-gravado: lead
// 'clicou' sem linha em `clique` faria a metrica de conversao do disparo
// mentir para a diretoria comercial.
//
// Best-effort de proposito: se falhar, a pagina abre do mesmo jeito. O
// cliente ja demonstrou interesse; travar o caminho dele ate o WhatsApp
// por causa de uma falha nossa de gravacao custa o lead inteiro, enquanto
// perder o registro custa uma linha de metrica.
func (h *Transbordo) registrarClique(r *http.Request, token string, leadID int64) {
	ctx := r.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("transbordo: iniciar transacao do clique", "token", token, "erro", err)
		return
	}
	defer tx.Rollback(ctx)

	queries := store.New(tx)

	if err := queries.RegistrarClique(ctx, store.RegistrarCliqueParams{
		Token:  token,
		LeadID: &leadID,
		// IP e user agent entram como evidencia do clique. RemoteAddr vem
		// com porta e, atras do proxy reverso, e o IP do proxy -- serve
		// para auditoria, nao para geolocalizar.
		Ip:        naoVazio(r.RemoteAddr),
		UserAgent: naoVazio(r.UserAgent()),
	}); err != nil {
		slog.Error("transbordo: registrar clique", "token", token, "erro", err)
		return
	}

	if err := queries.MarcarDisparoClicado(ctx, token); err != nil {
		slog.Error("transbordo: marcar disparo como clicado", "token", token, "erro", err)
		return
	}

	if err := queries.AvancarEstadoDoLead(ctx, store.AvancarEstadoDoLeadParams{
		ID:     leadID,
		Estado: "clicou",
	}); err != nil {
		slog.Error("transbordo: avancar estado do lead para clicou", "lead_id", leadID, "erro", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("transbordo: commit do clique", "token", token, "erro", err)
	}
}

// montarTextoTransbordo preenche o texto do wa.me com o codigo do
// empreendimento -- o token tambem serve de fallback pro matcher (regra
// 4 da secao 6, quando o cliente so aperta enviar sem editar o texto).
func montarTextoTransbordo(nomeEmpreendimento, token string) string {
	return fmt.Sprintf("Olá! Tenho interesse em %s (código %s)", nomeEmpreendimento, token)
}

type dadosPaginaTransbordo struct {
	NomeEmpreendimento string
	LinkWhatsapp       string
}

var paginaTransbordo = template.Must(template.New("transbordo").Parse(`<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.NomeEmpreendimento}}</title>
<style>
  body { font-family: system-ui, sans-serif; background: #f5f5f5; margin: 0; padding: 2rem 1rem; text-align: center; }
  .cartao { max-width: 420px; margin: 3rem auto; background: #fff; border-radius: 12px; padding: 2rem; box-shadow: 0 2px 12px rgba(0,0,0,.08); }
  h1 { font-size: 1.25rem; margin-bottom: .5rem; }
  p { color: #555; }
  a.botao { display: inline-block; margin-top: 1.5rem; padding: .9rem 1.75rem; background: #25D366; color: #fff; text-decoration: none; border-radius: 8px; font-weight: 600; }
</style>
</head>
<body>
  <div class="cartao">
    <h1>{{.NomeEmpreendimento}}</h1>
    <p>Fale agora com um consultor sobre este empreendimento.</p>
    <a class="botao" href="{{.LinkWhatsapp}}">Falar com consultor</a>
  </div>
</body>
</html>
`))
