// package retencao apaga periodicamente o que nao tem mais consumidor:
// historico de saude do provedor e payload bruto de webhook (fase 6).
//
// Roda dentro do gateway, e nao como job do scheduler do CRM, por dois
// motivos: as duas tabelas sao escritas so pelo gateway, e o CRM nao deve
// ter DDL/DELETE no schema (decisao D-6, usuario Postgres restrito).
package retencao

import (
	"context"
	"log/slog"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Repositorio e o subconjunto de store.Queries que o monitor precisa.
type Repositorio interface {
	PurgarProvedorSaudeAntigo(ctx context.Context, retencaoSegundos float64) (int64, error)
	PurgarPayloadBrutoAntigo(ctx context.Context, retencaoSegundos float64) (int64, error)
}

type Config struct {
	// Intervalo entre passadas. Diario: a limpeza nao tem urgencia, e
	// rodar de hora em hora so gastaria I/O em cima de tabela grande.
	Intervalo time.Duration
	// RetencaoSaude e por quanto tempo guardar as leituras de saude que
	// NAO sao transicao de estado (ver a query -- transicoes ficam).
	RetencaoSaude time.Duration
	// RetencaoPayloadBruto e por quanto tempo guardar o payload cru dos
	// webhooks. Curto de proposito: serve para depurar recepcao recente,
	// e o dado de negocio ja esta nas tabelas de dominio.
	RetencaoPayloadBruto time.Duration
}

func (c Config) comDefaults() Config {
	if c.Intervalo <= 0 {
		c.Intervalo = 24 * time.Hour
	}
	if c.RetencaoSaude <= 0 {
		c.RetencaoSaude = 30 * 24 * time.Hour
	}
	if c.RetencaoPayloadBruto <= 0 {
		c.RetencaoPayloadBruto = 90 * 24 * time.Hour
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

// Executar roda uma passada na subida e depois uma por tick. A passada
// inicial existe para um gateway que fique semanas sem reiniciar nao ser o
// unico momento de limpeza -- e para a primeira execucao em base antiga
// limpar o acumulo de uma vez.
func (m *Monitor) Executar(ctx context.Context) error {
	m.passar(ctx)

	ticker := time.NewTicker(m.cfg.Intervalo)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.passar(ctx)
		}
	}
}

// passar nunca devolve erro: falha de limpeza nao pode derrubar o gateway
// nem parar o loop -- mesmo padrao do outbox e do monitor de saude.
func (m *Monitor) passar(ctx context.Context) {
	if n, err := m.repo.PurgarProvedorSaudeAntigo(ctx, m.cfg.RetencaoSaude.Seconds()); err != nil {
		slog.Error("retencao: purgar provedor_saude", "erro", err)
	} else if n > 0 {
		slog.Info("retencao: provedor_saude limpo", "linhas", n, "retencao", m.cfg.RetencaoSaude.String())
	}

	if n, err := m.repo.PurgarPayloadBrutoAntigo(ctx, m.cfg.RetencaoPayloadBruto.Seconds()); err != nil {
		slog.Error("retencao: purgar lead_payload_bruto", "erro", err)
	} else if n > 0 {
		slog.Info("retencao: lead_payload_bruto limpo", "linhas", n, "retencao", m.cfg.RetencaoPayloadBruto.String())
	}
}

// garante em tempo de compilacao que store.Queries continua satisfazendo
// Repositorio -- se o sqlc regenerar com assinatura diferente, quebra aqui
// em vez de so no wiring do main.
var _ Repositorio = (*store.Queries)(nil)
