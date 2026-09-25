package caixa

import (
	"context"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
)

type chaveContexto struct{}

// NoContexto guarda a caixa resolvida pelo webhook para o handler.
func NoContexto(ctx context.Context, c Caixa) context.Context {
	return context.WithValue(ctx, chaveContexto{}, c)
}

// DoContexto devolve a caixa que o middleware do webhook resolveu.
func DoContexto(ctx context.Context) (Caixa, bool) {
	c, ok := ctx.Value(chaveContexto{}).(Caixa)
	return c, ok
}

// ComProvedores e o que o outbox e o monitor de saude precisam: as caixas
// ativas e o provedor de cada uma.
type ComProvedores interface {
	Ativas(ctx context.Context) ([]Caixa, error)
	Provedor(c Caixa) provedor.Provedor
}

var _ ComProvedores = (*Registro)(nil)

// ComProvedor liga uma caixa a um provedor ja montado.
type ComProvedor struct {
	Caixa    Caixa
	Provedor provedor.Provedor
}

// Fixas e um conjunto fechado de caixas, sem banco -- para testes.
type Fixas []ComProvedor

// Unica e a caixa de teste de quem nao trata de multi-caixa.
func Unica(p provedor.Provedor) Fixas {
	return Fixas{{Caixa: Nova(1, "teste", ProvedorFake), Provedor: p}}
}

func (f Fixas) Ativas(context.Context) ([]Caixa, error) {
	caixas := make([]Caixa, 0, len(f))
	for _, c := range f {
		caixas = append(caixas, c.Caixa)
	}
	return caixas, nil
}

func (f Fixas) Provedor(c Caixa) provedor.Provedor {
	for _, cp := range f {
		if cp.Caixa.ID == c.ID {
			return cp.Provedor
		}
	}
	return nil
}
