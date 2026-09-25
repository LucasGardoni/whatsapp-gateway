// package fake e um provedor.Provedor em memoria para testes do outbox
// (fase 3) sem depender da rede ou de credenciais reais.
package fake

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
)

type Provedor struct {
	mu sync.Mutex

	// prefixo torna o id devolvido unico entre instancias: provedor_msg_id e
	// unico no banco inteiro, e duas caixas fake (G1) ou duas execucoes
	// contra o mesmo banco devolveriam o mesmo "fake-1".
	prefixo string

	Enviados      []provedor.MensagemTexto
	EnviadosMidia []provedor.MensagemMidia
	StatusAtual   provedor.StatusInstancia

	// ProximoErro, se definido, e devolvido pela proxima chamada a Enviar
	// ou EnviarMidia e depois limpo -- simula uma falha pontual (ex.: shadowban).
	ProximoErro error
}

var instancias atomic.Int64

func Novo() *Provedor {
	return &Provedor{
		prefixo:     strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(instancias.Add(1), 36),
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

	return &provedor.ResultadoEnvio{MessageID: fmt.Sprintf("fake-%s-%d", p.prefixo, len(p.Enviados))}, nil
}

func (p *Provedor) EnviarMidia(ctx context.Context, msg provedor.MensagemMidia) (*provedor.ResultadoEnvio, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.EnviadosMidia = append(p.EnviadosMidia, msg)

	if p.ProximoErro != nil {
		erro := p.ProximoErro
		p.ProximoErro = nil
		return nil, erro
	}

	return &provedor.ResultadoEnvio{MessageID: fmt.Sprintf("fake-midia-%s-%d", p.prefixo, len(p.EnviadosMidia))}, nil
}

func (p *Provedor) Status(ctx context.Context) (*provedor.StatusInstancia, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	status := p.StatusAtual
	return &status, nil
}
