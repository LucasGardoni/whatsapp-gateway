// package caixa resolve os numeros de WhatsApp do gateway (G1 de
// docs/PLANO_MULTICAIXA_E_CONVERSAS.md).
//
// Ate o G1 havia um cliente z-api global do processo, montado do .env. Agora
// cada caixa tem as proprias credenciais no banco, e quem precisa falar com
// o provedor pede o cliente DA CAIXA -- o outbox pela caixa da conversa, o
// webhook pelo segredo do path, as rotas /v1 pelo codigo da query.
//
// O erro que este pacote existe para impedir e mandar mensagem da Franco
// pelo numero da Lider: visivel para o cliente final e sem desfazer.
package caixa

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/fake"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// ErrDesconhecida e o codigo ou id que nao casa com caixa ativa.
var ErrDesconhecida = errors.New("caixa desconhecida")

// Tipos de provedor aceitos pelo CHECK de caixa.provedor.
const (
	ProvedorZAPI = "zapi"
	ProvedorFake = "fake"
)

// Caixa e a linha de `caixa` sem as credenciais expostas: quem precisa do
// provedor pede ao Registro, e nenhum handler le token.
type Caixa struct {
	ID       int64
	Codigo   string
	Nome     string
	Provedor string

	instanciaID    string
	instanciaToken string
	clientToken    string
	webhookSegredo string
}

// Nova monta uma caixa sem credencial -- para testes e para provedor fake.
func Nova(id int64, codigo, tipoProvedor string) Caixa {
	return Caixa{ID: id, Codigo: codigo, Nome: codigo, Provedor: tipoProvedor}
}

func deStore(l store.Caixa) Caixa {
	return Caixa{
		ID:             l.ID,
		Codigo:         l.Codigo,
		Nome:           l.Nome,
		Provedor:       l.Provedor,
		instanciaID:    deref(l.InstanciaID),
		instanciaToken: deref(l.InstanciaToken),
		clientToken:    deref(l.ClientToken),
		webhookSegredo: l.WebhookSegredo,
	}
}

// credenciais identifica a versao das credenciais: trocar o token no banco
// troca a chave, e o cliente antigo e descartado.
func (c Caixa) credenciais() string {
	return c.Provedor + "\x00" + c.instanciaID + "\x00" + c.instanciaToken + "\x00" + c.clientToken
}

// Consultas e o que o Registro le do banco.
type Consultas interface {
	ListarCaixasAtivas(ctx context.Context) ([]store.Caixa, error)
}

// Registro guarda as caixas ativas em memoria com TTL curto: cadastrar ou
// desativar um numero vale sem reiniciar o gateway, e o webhook nao vai ao
// banco a cada mensagem.
type Registro struct {
	consultas Consultas
	padrao    string
	ttl       time.Duration
	// recargaMinima segura o segredo errado: sem ela, cada POST com segredo
	// inventado forcaria uma leitura do banco.
	recargaMinima time.Duration
	baseURLZAPI   string

	mu          sync.Mutex
	carregadoEm time.Time
	caixas      []Caixa
	clientes    map[int64]clientesDaCaixa
	fakes       map[int64]*fake.Provedor
}

type clientesDaCaixa struct {
	chave      string
	zapi       *zapi.Cliente
	identidade *identidade.Cliente
}

// NovoRegistro recebe o codigo da caixa padrao (CAIXA_CODIGO): a que vale
// quando o chamador nao diz qual.
func NovoRegistro(consultas Consultas, padrao string) *Registro {
	return &Registro{
		consultas:     consultas,
		padrao:        padrao,
		ttl:           30 * time.Second,
		recargaMinima: 5 * time.Second,
		clientes:      map[int64]clientesDaCaixa{},
		fakes:         map[int64]*fake.Provedor{},
	}
}

// ComBaseZAPI aponta os clientes z-api para outro host -- testes contra
// servidor falso.
func (r *Registro) ComBaseZAPI(baseURL string) *Registro {
	r.baseURLZAPI = baseURL
	return r
}

// Padrao e o codigo da caixa usada quando o chamador nao informa uma.
func (r *Registro) Padrao() string { return r.padrao }

// Ativas devolve as caixas ativas, recarregando se o cache venceu.
func (r *Registro) Ativas(ctx context.Context) ([]Caixa, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.carregar(ctx, false); err != nil {
		return nil, err
	}
	return append([]Caixa(nil), r.caixas...), nil
}

// PorCodigo resolve a caixa pelo codigo. Codigo vazio e a caixa padrao.
func (r *Registro) PorCodigo(ctx context.Context, codigo string) (Caixa, error) {
	if codigo == "" {
		codigo = r.padrao
	}
	return r.buscar(ctx, func(c Caixa) bool { return c.Codigo == codigo })
}

// PorID resolve a caixa pelo id.
func (r *Registro) PorID(ctx context.Context, id int64) (Caixa, error) {
	return r.buscar(ctx, func(c Caixa) bool { return c.ID == id })
}

// PorSegredo resolve a caixa do webhook pelo segmento do path. A comparacao
// e em tempo constante contra cada caixa, como fazia o segredo unico.
func (r *Registro) PorSegredo(ctx context.Context, segredo string) (Caixa, bool, error) {
	if segredo == "" {
		return Caixa{}, false, nil
	}
	c, err := r.buscar(ctx, func(c Caixa) bool {
		return subtle.ConstantTimeCompare([]byte(c.webhookSegredo), []byte(segredo)) == 1
	})
	if errors.Is(err, ErrDesconhecida) {
		return Caixa{}, false, nil
	}
	if err != nil {
		return Caixa{}, false, err
	}
	return c, true, nil
}

// buscar procura no cache e, sem achar, recarrega uma vez -- a caixa pode
// ter sido cadastrada depois da ultima leitura.
func (r *Registro) buscar(ctx context.Context, casa func(Caixa) bool) (Caixa, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.carregar(ctx, false); err != nil {
		return Caixa{}, err
	}
	for _, c := range r.caixas {
		if casa(c) {
			return c, nil
		}
	}
	if time.Since(r.carregadoEm) < r.recargaMinima {
		return Caixa{}, ErrDesconhecida
	}
	if err := r.carregar(ctx, true); err != nil {
		return Caixa{}, err
	}
	for _, c := range r.caixas {
		if casa(c) {
			return c, nil
		}
	}
	return Caixa{}, ErrDesconhecida
}

// carregar exige r.mu. Falha de leitura com cache ja carregado mantem o
// cache: um soluco do banco nao pode fazer todo webhook responder 404.
func (r *Registro) carregar(ctx context.Context, forcar bool) error {
	if !forcar && !r.carregadoEm.IsZero() && time.Since(r.carregadoEm) < r.ttl {
		return nil
	}
	linhas, err := r.consultas.ListarCaixasAtivas(ctx)
	if err != nil {
		if r.carregadoEm.IsZero() {
			return fmt.Errorf("listar caixas: %w", err)
		}
		slog.Warn("caixa: falha ao recarregar, mantendo o cache anterior", "erro", err)
		return nil
	}
	caixas := make([]Caixa, 0, len(linhas))
	for _, l := range linhas {
		caixas = append(caixas, deStore(l))
	}
	r.caixas = caixas
	r.carregadoEm = time.Now()
	return nil
}

// Provedor devolve o provedor de envio da caixa. Caixa fake tem um
// fake.Provedor proprio, que guarda o que "enviou" -- serve para ter um
// segundo numero em dev sem uma segunda instancia z-api.
func (r *Registro) Provedor(c Caixa) provedor.Provedor {
	if c.Provedor == ProvedorFake {
		return r.Fake(c)
	}
	return r.clientesDe(c).zapi
}

// Fake devolve o provedor em memoria da caixa fake.
func (r *Registro) Fake(c Caixa) *fake.Provedor {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.fakes[c.ID]
	if !ok {
		p = fake.Novo()
		r.fakes[c.ID] = p
	}
	return p
}

// ZAPI devolve o cliente z-api concreto (fila, qr code, agenda, chamadas).
// false para caixa que nao e z-api.
func (r *Registro) ZAPI(c Caixa) (*zapi.Cliente, bool) {
	if c.Provedor != ProvedorZAPI {
		return nil, false
	}
	return r.clientesDe(c).zapi, true
}

// Identidade devolve o resolvedor de @lid da caixa. false para caixa que
// nao e z-api: a abertura segue sem @lid, como quando a resolucao falha.
func (r *Registro) Identidade(c Caixa) (*identidade.Cliente, bool) {
	if c.Provedor != ProvedorZAPI {
		return nil, false
	}
	return r.clientesDe(c).identidade, true
}

func (r *Registro) clientesDe(c Caixa) clientesDaCaixa {
	r.mu.Lock()
	defer r.mu.Unlock()
	chave := c.credenciais()
	if cl, ok := r.clientes[c.ID]; ok && cl.chave == chave {
		return cl
	}
	cl := clientesDaCaixa{chave: chave}
	if r.baseURLZAPI != "" {
		cl.zapi = zapi.NovoClienteComBase(r.baseURLZAPI, c.instanciaID, c.instanciaToken, c.clientToken)
		cl.identidade = identidade.NovoClienteComBase(r.baseURLZAPI, c.instanciaID, c.instanciaToken, c.clientToken)
	} else {
		cl.zapi = zapi.NovoCliente(c.instanciaID, c.instanciaToken, c.clientToken)
		cl.identidade = identidade.NovoCliente(c.instanciaID, c.instanciaToken, c.clientToken)
	}
	r.clientes[c.ID] = cl
	return cl
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
