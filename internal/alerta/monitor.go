// package alerta detecta volume anormal de envio (fase 12) -- fator no 1
// de risco de banimento documentado na secao 4.8: "numero de destinatarios
// distintos em curto periodo". So detecta e registra; notificar de
// verdade (e-mail, escalonamento) e o CRM que faz lendo a tabela `alerta`
// direto do Postgres, mesmo padrao de provedor_saude/dlp_ocorrencia/
// sla_evento -- nao precisa de endpoint novo pra isso.
package alerta

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

const TipoVolumeAnormal = "volume_anormal"

// TipoVolumeAnormalAplicacao e o alerta da fase 9 do barramento: uma
// aplicacao saiu do comportamento DELA.
//
// Tipo proprio, e nao o mesmo TipoVolumeAnormal com aplicacao_id
// preenchido: os dois medem coisas diferentes. Aquele mede risco de
// banimento do numero (destinatarios distintos no WhatsApp, compartilhado
// pela empresa); este mede uma integracao em laco. Um tipo unico faria o
// supervisor ler as duas coisas na mesma fila sem saber qual agir.
const TipoVolumeAnormalAplicacao = "volume_anormal_aplicacao"

// Repositorio e o subconjunto de store.Queries que o monitor precisa.
type Repositorio interface {
	ContarDestinatariosDistintosNaJanela(ctx context.Context, janelaSegundos float64) (int64, error)
	MedirVolumePorAplicacao(ctx context.Context, arg store.MedirVolumePorAplicacaoParams) ([]store.MedirVolumePorAplicacaoRow, error)
	BuscarAlertaRecente(ctx context.Context, arg store.BuscarAlertaRecenteParams) (store.Alertum, error)
	RegistrarAlerta(ctx context.Context, arg store.RegistrarAlertaParams) error
}

type Config struct {
	// Intervalo entre verificacoes.
	Intervalo time.Duration
	// Janela de tempo em que se conta destinatarios distintos.
	Janela time.Duration
	// LimiteDestinatarios acima do qual o volume e considerado anormal.
	// Sem valor fechado no plano -- default conservador, ajustavel sem
	// redeploy no futuro se isso passar a ler de `parametro` tambem.
	LimiteDestinatarios int64

	// Os tres campos abaixo governam o alerta por aplicacao (fase 9 do
	// barramento).

	// JanelaBase e o periodo de onde sai a media de comparacao. 24h por
	// default: pega o ciclo de um dia inteiro, entao a manha nao e
	// comparada so com a madrugada.
	JanelaBase time.Duration
	// FatorSobreMedia e o "N x a media" do plano.
	FatorSobreMedia float64
	// MinimoParaAlertar e o piso absoluto de mensagens na janela.
	//
	// Sem ele o alerta e inutil e barulhento ao mesmo tempo: uma aplicacao
	// que manda 2 mensagens por hora tem media ~0,08 por janela de 30min,
	// entao UMA mensagem ja e mais de 4x a media. O piso e o que
	// distingue "trafego baixo" de "laco".
	MinimoParaAlertar int64
}

func (c Config) comDefaults() Config {
	if c.Intervalo <= 0 {
		c.Intervalo = 5 * time.Minute
	}
	if c.Janela <= 0 {
		c.Janela = 30 * time.Minute
	}
	if c.LimiteDestinatarios <= 0 {
		c.LimiteDestinatarios = 30
	}
	if c.JanelaBase <= 0 {
		c.JanelaBase = 24 * time.Hour
	}
	if c.FatorSobreMedia <= 0 {
		c.FatorSobreMedia = 4
	}
	if c.MinimoParaAlertar <= 0 {
		c.MinimoParaAlertar = 50
	}
	// base menor que a janela tornaria a media um numero sem sentido (a
	// propria janela dividida por menos de 1). Nao e configuracao
	// invalida a ponto de recusar a subida -- e so um ajuste de quem
	// mexeu em um dos dois e esqueceu do outro.
	if c.JanelaBase < c.Janela {
		c.JanelaBase = c.Janela
	}
	return c
}

type Monitor struct {
	repo Repositorio
	cfg  Config
}

func NovoMonitor(repo Repositorio, cfg Config) *Monitor {
	return &Monitor{repo: repo, cfg: cfg.comDefaults()}
}

func (m *Monitor) Executar(ctx context.Context) error {
	ticker := time.NewTicker(m.cfg.Intervalo)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.verificar(ctx)
			m.verificarPorAplicacao(ctx)
		}
	}
}

// verificar nunca deixa o monitor parar por causa de uma falha de
// consulta -- so loga e tenta de novo no proximo tick, mesmo padrao do
// outbox/saude.
func (m *Monitor) verificar(ctx context.Context) {
	// janela como intervalo nos dois: o corte sai de LOCALTIMESTAMP no SQL,
	// no mesmo relogio que gravou criado_em (P1-08).
	janelaSegundos := m.cfg.Janela.Seconds()
	total, err := m.repo.ContarDestinatariosDistintosNaJanela(ctx, janelaSegundos)
	if err != nil {
		slog.Error("alerta: contar destinatarios distintos", "erro", err)
		return
	}
	if total < m.cfg.LimiteDestinatarios {
		return
	}

	// debounce: nao registra um alerta novo se ja existe um dentro da
	// mesma janela -- senao cada tick com o volume ainda alto gera uma
	// linha nova, poluindo o que o supervisor le.
	_, err = m.repo.BuscarAlertaRecente(ctx, store.BuscarAlertaRecenteParams{
		Tipo:           TipoVolumeAnormal,
		JanelaSegundos: janelaSegundos,
	})
	if err == nil {
		return
	}
	if err != pgx.ErrNoRows {
		slog.Error("alerta: buscar alerta recente", "erro", err)
		return
	}

	detalhe := fmt.Sprintf("%d destinatarios distintos nos ultimos %s (limite: %d)", total, m.cfg.Janela, m.cfg.LimiteDestinatarios)
	slog.Warn("alerta: volume anormal de destinatarios distintos", "total", total, "limite", m.cfg.LimiteDestinatarios, "janela", m.cfg.Janela)
	if err := m.repo.RegistrarAlerta(ctx, store.RegistrarAlertaParams{Tipo: TipoVolumeAnormal, Detalhe: &detalhe}); err != nil {
		slog.Error("alerta: registrar alerta", "erro", err)
	}
}
