package ingestao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// JanelaDedupPadrao vale quando o parametro `ingestao_dedup_janela_horas`
// (tabela parametro) nao esta configurado -- ver Config no handler.
const JanelaDedupPadrao = 72 * time.Hour

// Repositorio e o subconjunto de store.Queries que a deduplicacao precisa.
type Repositorio interface {
	BuscarLeadRecenteParaDedup(ctx context.Context, arg store.BuscarLeadRecenteParaDedupParams) (store.Lead, error)
	CriarLeadDeIngestao(ctx context.Context, arg store.CriarLeadDeIngestaoParams) (store.Lead, error)
}

type Resultado struct {
	LeadID int64
	Novo   bool
}

// ResolverOuCriarLead casa telefone + empreendimento dentro da janela
// (secao "Deduplicacao" da fase 11) antes de criar um lead novo. Sem
// telefone normalizado, dedup nao tem chave pra casar -- sempre cria novo.
func ResolverOuCriarLead(ctx context.Context, repo Repositorio, e Entrada, janela time.Duration) (Resultado, error) {
	if e.TelefoneE164 != "" {
		// janela como intervalo: o corte sai de LOCALTIMESTAMP no SQL, no
		// mesmo relogio que gravou criado_em (P1-08).
		lead, err := repo.BuscarLeadRecenteParaDedup(ctx, store.BuscarLeadRecenteParaDedupParams{
			TelefoneE164:     &e.TelefoneE164,
			EmpreendimentoID: e.EmpreendimentoID,
			JanelaSegundos:   janela.Seconds(),
		})
		switch {
		case err == nil:
			return Resultado{LeadID: lead.ID}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return Resultado{}, fmt.Errorf("ingestao: buscar lead para dedup: %w", err)
		}
	}

	lead, err := repo.CriarLeadDeIngestao(ctx, store.CriarLeadDeIngestaoParams{
		Nome:             naoVazio(e.Nome),
		TelefoneE164:     naoVazio(e.TelefoneE164),
		Origem:           naoVazio(e.Origem),
		EmpreendimentoID: e.EmpreendimentoID,
		AdSourceID:       naoVazio(e.AdSourceID),
		CtwaClid:         naoVazio(e.CtwaClid),
	})
	if err != nil {
		return Resultado{}, fmt.Errorf("ingestao: criar lead: %w", err)
	}
	return Resultado{LeadID: lead.ID, Novo: true}, nil
}

func naoVazio(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
