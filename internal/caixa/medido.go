package caixa

import (
	"context"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
)

// ObservadorProvedor recebe a duracao e o erro de cada chamada ao provedor
// (painel de observabilidade).
type ObservadorProvedor interface {
	ChamadaProvedor(caixa, operacao string, duracao time.Duration, err error)
}

// ComObservador mede toda chamada feita pelo provedor que Provedor devolve.
// Os clientes concretos (ZAPI, Identidade) ficam de fora: fila, qr code e
// agenda sao acionados a mao, nao sao o trafego que pesa.
func (r *Registro) ComObservador(o ObservadorProvedor) *Registro {
	r.observador = o
	return r
}

type provedorMedido struct {
	provedor.Provedor
	caixa      string
	observador ObservadorProvedor
}

func (p provedorMedido) Enviar(ctx context.Context, msg provedor.MensagemTexto) (*provedor.ResultadoEnvio, error) {
	inicio := time.Now()
	r, err := p.Provedor.Enviar(ctx, msg)
	p.observador.ChamadaProvedor(p.caixa, "enviar", time.Since(inicio), err)
	return r, err
}

func (p provedorMedido) EnviarMidia(ctx context.Context, msg provedor.MensagemMidia) (*provedor.ResultadoEnvio, error) {
	inicio := time.Now()
	r, err := p.Provedor.EnviarMidia(ctx, msg)
	p.observador.ChamadaProvedor(p.caixa, "enviar_midia", time.Since(inicio), err)
	return r, err
}

func (p provedorMedido) Status(ctx context.Context) (*provedor.StatusInstancia, error) {
	inicio := time.Now()
	r, err := p.Provedor.Status(ctx)
	p.observador.ChamadaProvedor(p.caixa, "status", time.Since(inicio), err)
	return r, err
}
