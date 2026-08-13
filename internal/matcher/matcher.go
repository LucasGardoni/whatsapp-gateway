// package matcher identifica o lead dono de uma mensagem recebida, seguindo
// as 5 regras da secao 6 do plano, na ordem de confianca ali definida.
// Falha em silencio e o pior resultado possivel aqui -- ver diretriz 6.
package matcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// janelaCliqueRecente define o que conta como "clique recente" na regra 2
// (secao 6). Nao ha valor fechado no plano; 24h foi a decisao adotada.
const janelaCliqueRecente = 24 * time.Hour

// Regra identifica qual das 5 regras da secao 6 decidiu o resultado --
// util para log e para fases futuras (SLA, atribuicao de empreendimento).
type Regra int

const (
	RegraChatLid Regra = iota
	RegraTelefoneComCliqueRecente
	RegraTelefoneSemCliqueRecente
	RegraToken
	RegraLeadNovo
)

// Repositorio e o subconjunto de store.Queries que o matcher precisa.
type Repositorio interface {
	BuscarLeadPorChatLid(ctx context.Context, chatLid *string) (store.Lead, error)
	BuscarLeadPorTelefone(ctx context.Context, telefoneE164 *string) (store.Lead, error)
	BuscarCliqueRecentePorLead(ctx context.Context, arg store.BuscarCliqueRecentePorLeadParams) (store.Clique, error)
	BuscarLeadPorTokenNoTexto(ctx context.Context, texto string) (store.Lead, error)
	CriarLead(ctx context.Context, arg store.CriarLeadParams) (store.Lead, error)
	PreencherChatLidSeVazio(ctx context.Context, arg store.PreencherChatLidSeVazioParams) error
}

// Entrada e o que o webhook consegue extrair do payload antes de saber o
// lead. Phone vem exatamente como a z-api mandou -- pode ser @lid
// (secao 4.3), o matcher e quem decide o que fazer com isso.
type Entrada struct {
	ChatLid     string
	Phone       string
	Texto       string
	NomeExibido string
}

type Resultado struct {
	LeadID int64
	Novo   bool
	Regra  Regra
}

func Resolver(ctx context.Context, repo Repositorio, e Entrada) (Resultado, error) {
	chatLidCandidato := e.ChatLid
	if chatLidCandidato == "" && identidade.EhLid(e.Phone) {
		chatLidCandidato = e.Phone
	}

	if chatLidCandidato != "" {
		lead, err := repo.BuscarLeadPorChatLid(ctx, &chatLidCandidato)
		switch {
		case err == nil:
			return Resultado{LeadID: lead.ID, Regra: RegraChatLid}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return Resultado{}, fmt.Errorf("matcher: buscar lead por chat_lid: %w", err)
		}
	}

	var telefoneNormalizado string
	if e.Phone != "" && !identidade.EhLid(e.Phone) {
		if n, err := identidade.NormalizarE164(e.Phone); err == nil {
			telefoneNormalizado = n
			lead, err := repo.BuscarLeadPorTelefone(ctx, &telefoneNormalizado)
			switch {
			case err == nil:
				if err := adotarChatLid(ctx, repo, lead, chatLidCandidato); err != nil {
					return Resultado{}, err
				}
				return resultadoPorTelefone(ctx, repo, lead)
			case !errors.Is(err, pgx.ErrNoRows):
				return Resultado{}, fmt.Errorf("matcher: buscar lead por telefone: %w", err)
			}
		}
	}

	if e.Texto != "" {
		lead, err := repo.BuscarLeadPorTokenNoTexto(ctx, e.Texto)
		switch {
		case err == nil:
			if err := adotarChatLid(ctx, repo, lead, chatLidCandidato); err != nil {
				return Resultado{}, err
			}
			return Resultado{LeadID: lead.ID, Regra: RegraToken}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return Resultado{}, fmt.Errorf("matcher: buscar lead por token no texto: %w", err)
		}
	}

	lead, err := repo.CriarLead(ctx, store.CriarLeadParams{
		Nome:         naoVazio(e.NomeExibido),
		TelefoneE164: naoVazio(telefoneNormalizado),
		ChatLid:      naoVazio(chatLidCandidato),
		Origem:       naoVazio("desconhecida"),
	})
	if err != nil {
		return Resultado{}, fmt.Errorf("matcher: criar lead novo: %w", err)
	}
	return Resultado{LeadID: lead.ID, Novo: true, Regra: RegraLeadNovo}, nil
}

// adotarChatLid grava no lead o @lid que veio no payload quando ele foi casado
// por telefone ou token e ainda nao tinha um. chat_lid e a identidade primaria
// (secao 4.3) e o telefone e o que a z-api pode parar de mandar a qualquer
// momento -- sem esse backfill, a proxima mensagem com phone ocultado nao casa
// pela regra 1 e o contato vira um lead novo, partindo o historico.
func adotarChatLid(ctx context.Context, repo Repositorio, lead store.Lead, chatLid string) error {
	if chatLid == "" || lead.ChatLid != nil {
		return nil
	}
	if err := repo.PreencherChatLidSeVazio(ctx, store.PreencherChatLidSeVazioParams{
		ID:      lead.ID,
		ChatLid: &chatLid,
	}); err != nil {
		return fmt.Errorf("matcher: preencher chat_lid do lead %d: %w", lead.ID, err)
	}
	return nil
}

// resultadoPorTelefone distingue a regra 2 da regra 3 -- a diferenca e so a
// classificacao de confianca (clique recente ou nao); o lead e o mesmo.
func resultadoPorTelefone(ctx context.Context, repo Repositorio, lead store.Lead) (Resultado, error) {
	// a janela vai como intervalo -- o corte e feito com LOCALTIMESTAMP no
	// proprio SQL, no mesmo relogio que gravou clicado_em (P1-08).
	_, err := repo.BuscarCliqueRecentePorLead(ctx, store.BuscarCliqueRecentePorLeadParams{
		LeadID:         &lead.ID,
		JanelaSegundos: janelaCliqueRecente.Seconds(),
	})
	switch {
	case err == nil:
		return Resultado{LeadID: lead.ID, Regra: RegraTelefoneComCliqueRecente}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return Resultado{LeadID: lead.ID, Regra: RegraTelefoneSemCliqueRecente}, nil
	default:
		return Resultado{}, fmt.Errorf("matcher: buscar clique recente: %w", err)
	}
}

func naoVazio(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
