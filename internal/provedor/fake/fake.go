// package fake e um provedor.Provedor em memoria para testes do outbox
// (fase 3) sem depender da rede ou de credenciais reais.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
)

type Provedor struct {
	mu sync.Mutex

	Enviados    []provedor.MensagemTexto
	StatusAtual provedor.StatusInstancia

	// ProximoErro, se definido, e devolvido pela proxima chamada a Enviar
	// e depois limpo -- simula uma falha pontual (ex.: shadowban).
	ProximoErro error
}

func Novo() *Provedor {
	return &Provedor{
		StatusAtual: provedor.StatusInstancia{Conectada: true},
	}
}

var _ provedor.Provedor = (*Provedor)(nil)

func (p *Provedor) Enviar(ctx context.Context, msg provedor.MensagemTexto) (*provedor.ResultadoEnvio, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Enviados = append(p.Enviados, msg)

	if p.ProximoErro != nil {
		erro := p.ProximoErro
		p.ProximoErro = nil
		return nil, erro
	}

	return &provedor.ResultadoEnvio{MessageID: fmt.Sprintf("fake-%d", len(p.Enviados))}, nil
}

func (p *Provedor) Status(ctx context.Context) (*provedor.StatusInstancia, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	status := p.StatusAtual
	return &status, nil
}
