package alerta

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// verificarPorAplicacao detecta a aplicacao que saiu do comportamento
// dela mesma (barramento, fase 9).
//
// Por que contra a media DA PROPRIA aplicacao e nao contra as outras: o
// CRM manda ordens de magnitude mais que uma plataforma nova, e comparar
// as duas diria so isso. O que se quer pegar e a integracao que dobrou de
// volume porque entrou em laco -- e isso so aparece contra o historico
// dela.
//
// Por que so DETECTA e registra: notificar de verdade (e-mail,
// escalonamento) e do CRM, que le a tabela `alerta` direto do Postgres --
// mesmo padrao de provedor_saude/dlp_ocorrencia/sla_evento. O gateway nao
// ganha endpoint novo nem cliente de SMTP por causa disto.
//
// O que ele NAO faz, e e deliberado: nao bloqueia. Quem bloqueia e o
// rate limit por aplicacao (middleware.LimitePorAplicacao), que e
// sincrono e tem teto configurado. Um monitor que desativasse a aplicacao
// sozinho transformaria um pico legitimo de fim de mes em incidente, e a
// decisao de cortar o trafego de uma plataforma e de quem opera.
func (m *Monitor) verificarPorAplicacao(ctx context.Context) {
	janelaSegundos := m.cfg.Janela.Seconds()
	baseSegundos := m.cfg.JanelaBase.Seconds()

	linhas, err := m.repo.MedirVolumePorAplicacao(ctx, store.MedirVolumePorAplicacaoParams{
		JanelaSegundos: janelaSegundos,
		BaseSegundos:   baseSegundos,
	})
	if err != nil {
		slog.Error("alerta: medir volume por aplicacao", "erro", err)
		return
	}

	// quantas janelas cabem na base -- o divisor da media. comDefaults
	// garante JanelaBase >= Janela, entao janelas >= 1 e nao ha divisao
	// por zero nem media inflada por um divisor fracionario.
	janelas := baseSegundos / janelaSegundos

	for _, l := range linhas {
		// a janela recente esta DENTRO da base (a consulta conta as mesmas
		// linhas), entao ela tem de sair antes da media. Sem isto, uma
		// rajada se compara com uma media que ela mesma levantou, e o
		// alerta fica mais surdo justamente quando o pico e maior.
		base := l.NaBase - l.NaJanela
		media := float64(base) / janelas

		if l.NaJanela < m.cfg.MinimoParaAlertar {
			continue
		}
		if float64(l.NaJanela) < m.cfg.FatorSobreMedia*media {
			continue
		}

		m.registrarAlertaDeAplicacao(ctx, l, media)
	}
}

func (m *Monitor) registrarAlertaDeAplicacao(ctx context.Context, l store.MedirVolumePorAplicacaoRow, media float64) {
	aplicacaoID := l.AplicacaoID

	// debounce por (tipo, aplicacao): sem o aplicacao_id no filtro, o
	// primeiro alerta de uma aplicacao calaria o de todas as outras pela
	// janela inteira -- e varias aplicacoes disparando junto e exatamente
	// o caso em que mais se precisa ver cada uma.
	_, err := m.repo.BuscarAlertaRecente(ctx, store.BuscarAlertaRecenteParams{
		Tipo:           TipoVolumeAnormalAplicacao,
		AplicacaoID:    &aplicacaoID,
		JanelaSegundos: m.cfg.Janela.Seconds(),
	})
	if err == nil {
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("alerta: buscar alerta recente da aplicacao", "aplicacao", l.Codigo, "erro", err)
		return
	}

	// o detalhe leva os tres numeros que sustentam a conclusao. Um alerta
	// que so diz "volume anormal" obriga quem le a refazer a consulta a
	// mao antes de decidir qualquer coisa.
	detalhe := fmt.Sprintf(
		"%d mensagens nos ultimos %s (media da propria aplicacao: %.1f por janela, fator: %.1fx, base: %s)",
		l.NaJanela, m.cfg.Janela, media, m.cfg.FatorSobreMedia, m.cfg.JanelaBase,
	)
	slog.Warn("alerta: volume anormal de uma aplicacao",
		"aplicacao", l.Codigo, "na_janela", l.NaJanela, "media", media, "fator", m.cfg.FatorSobreMedia)

	if err := m.repo.RegistrarAlerta(ctx, store.RegistrarAlertaParams{
		Tipo:        TipoVolumeAnormalAplicacao,
		Detalhe:     &detalhe,
		AplicacaoID: &aplicacaoID,
	}); err != nil {
		slog.Error("alerta: registrar alerta da aplicacao", "aplicacao", l.Codigo, "erro", err)
	}
}
