// package saude poll periodicamente o provedor e alimenta provedor_saude --
// o webhook on-whatsapp-disconnected (fase 4) so avisa quando a instancia
// cai, nunca confirma que ela voltou. Sem esse polling ativo, uma
// reconexao silenciosa nunca aparece no dashboard (fase 9).
package saude

import (
	"context"
	"log/slog"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Registrador e o subconjunto de store.Queries que o monitor precisa --
// mesmo padrao do outbox: testar a orquestracao sem depender de Postgres
// real.
type Registrador interface {
	RegistrarSaudeProvedor(ctx context.Context, arg store.RegistrarSaudeProvedorParams) error
}

type Config struct {
	// NomeProvedor identifica a linha em provedor_saude (ex.: "zapi").
	NomeProvedor string
	Intervalo    time.Duration
}

func (c Config) comDefaults() Config {
	if c.NomeProvedor == "" {
		c.NomeProvedor = "zapi"
	}
	if c.Intervalo <= 0 {
		c.Intervalo = 30 * time.Second
	}
	return c
}

type Monitor struct {
	provedor provedor.Provedor
	registro Registrador
	cfg      Config
}

func NovoMonitor(p provedor.Provedor, r Registrador, cfg Config) *Monitor {
	return &Monitor{provedor: p, registro: r, cfg: cfg.comDefaults()}
}

// Executar consulta o status a cada tick ate o contexto ser cancelado.
// Nunca retorna erro por falha de consulta -- isso e exatamente o que o
// monitor existe para detectar, nao um motivo pra parar de rodar.
func (m *Monitor) Executar(ctx context.Context) error {
	m.verificar(ctx)

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

func (m *Monitor) verificar(ctx context.Context) {
	inicio := time.Now()
	status, err := m.provedor.Status(ctx)
	latenciaMs := int32(time.Since(inicio).Milliseconds())

	arg := store.RegistrarSaudeProvedorParams{
		Provedor:   m.cfg.NomeProvedor,
		LatenciaMs: &latenciaMs,
	}
	if err != nil {
		slog.Warn("saude: falha ao consultar status do provedor", "provedor", m.cfg.NomeProvedor, "erro", err)
		motivo := err.Error()
		arg.Conectado = false
		arg.UltimoErro = &motivo
	} else {
		arg.Conectado = status.Conectada
		if status.Detalhe != "" {
			arg.UltimoErro = &status.Detalhe
		}
	}

	if err := m.registro.RegistrarSaudeProvedor(ctx, arg); err != nil {
		slog.Error("saude: falha ao registrar saude do provedor", "provedor", m.cfg.NomeProvedor, "erro", err)
	}
}
