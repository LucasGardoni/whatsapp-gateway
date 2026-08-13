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
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

const TipoVolumeAnormal = "volume_anormal"

// Repositorio e o subconjunto de store.Queries que o monitor precisa.
type Repositorio interface {
	ContarDestinatariosDistintosDesde(ctx context.Context, criadoEm pgtype.Timestamp) (int64, error)
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
		}
	}
}

// verificar nunca deixa o monitor parar por causa de uma falha de
// consulta -- so loga e tenta de novo no proximo tick, mesmo padrao do
// outbox/saude.
func (m *Monitor) verificar(ctx context.Context) {
	desde := time.Now().Add(-m.cfg.Janela)
	total, err := m.repo.ContarDestinatariosDistintosDesde(ctx, pgtype.Timestamp{Time: desde, Valid: true})
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
		Tipo:     TipoVolumeAnormal,
		CriadoEm: pgtype.Timestamp{Time: desde, Valid: true},
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
