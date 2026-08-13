// package chatinterno cobre o lado de tempo real do chat interno (fase
// 10). A escrita (canais, threads, DM, FAQ) e 100% CRM -- mensagem_interna
// nunca passa pelo gateway (secao 6 do plano). Este pacote so faz a leitura:
// polling periodico da tabela pra alimentar o mesmo hub sse que ja serve
// as mensagens de whatsapp (fase 7).
//
// Polling em vez de LISTEN/NOTIFY -- mais simples e suficiente pro tamanho
// do problema (5 corretores, secao 1); LISTEN/NOTIFY exigiria mais uma
// conexao dedicada e reconexao tratada a parte, custo maior do que o
// ganho aqui.
package chatinterno

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Leitor e o subconjunto de store.Queries que o poller precisa -- mesmo
// padrao do outbox/saude: testar a orquestracao sem depender de Postgres
// real.
type Leitor interface {
	BuscarUltimoIDMensagemInterna(ctx context.Context) (int64, error)
	ListarMensagensInternasAPartirDe(ctx context.Context, id int64) ([]store.MensagemInterna, error)
}

type Config struct {
	Intervalo time.Duration
}

func (c Config) comDefaults() Config {
	if c.Intervalo <= 0 {
		c.Intervalo = 3 * time.Second
	}
	return c
}

type Poller struct {
	leitor   Leitor
	hub      *sse.Hub
	cfg      Config
	ultimoID int64
}

func NovoPoller(leitor Leitor, hub *sse.Hub, cfg Config) *Poller {
	return &Poller{leitor: leitor, hub: hub, cfg: cfg.comDefaults()}
}

// Executar posiciona o cursor no maior id existente (nao replay o
// historico num restart) e entao verifica por linhas novas a cada tick,
// ate o contexto ser cancelado.
func (p *Poller) Executar(ctx context.Context) error {
	ultimoID, err := p.leitor.BuscarUltimoIDMensagemInterna(ctx)
	if err != nil {
		return fmt.Errorf("chatinterno: buscar cursor inicial: %w", err)
	}
	p.ultimoID = ultimoID

	ticker := time.NewTicker(p.cfg.Intervalo)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.verificar(ctx)
		}
	}
}

func (p *Poller) verificar(ctx context.Context) {
	mensagens, err := p.leitor.ListarMensagensInternasAPartirDe(ctx, p.ultimoID)
	if err != nil {
		slog.Error("chatinterno: listar mensagens novas", "erro", err)
		return
	}

	for _, m := range mensagens {
		p.hub.PublicarTodos(sse.Evento{
			Tipo:       sse.EventoMensagemInternaNova,
			MensagemID: m.ID,
			CanalID:    m.CanalID,
		})
		p.ultimoID = m.ID
	}
}
