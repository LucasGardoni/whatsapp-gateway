// package observabilidade mede o gateway por dentro: latencia por rota e
// por aplicacao, conexoes SSE, chamadas ao provedor, fila de saida, pool de
// banco e o batimento de cada rotina de fundo. Alimenta GET
// /api/observabilidade, que o painel do portal le.
//
// Segue as tres decisoes de internal/metrica: em memoria e por instancia,
// sem destino/canal/remetente em rotulo nenhum, e sem conhecer de antemao
// quais aplicacoes existem. A rota entra pelo PADRAO do chi
// ("/v1/conversas/{id}/mensagens"), nunca pelo path cru: o path cru tem id
// de conversa e, nos webhooks, o segredo da caixa.
package observabilidade

import (
	"sync"
	"sync/atomic"
	"time"
)

// estatistica e o par cumulativo + janela de uma serie de latencia.
type estatistica struct {
	total    histograma
	janela   *janelaHist
	erros4xx uint64
	erros5xx uint64
	// limitadas sao os 429: aplicacao batendo no teto, que e o sinal de
	// laco de integracao que o painel existe para mostrar.
	limitadas       uint64
	limitadasJanela contadorJanela
}

func novaEstatistica(limites []float64) *estatistica {
	return &estatistica{total: novoHistograma(limites), janela: novaJanelaHist(limites)}
}

func (e *estatistica) observar(agora time.Time, v float64, status int) {
	e.total.observar(v)
	e.janela.observar(agora, v, status >= 500)
	switch {
	case status == 429:
		e.limitadas++
		e.limitadasJanela.somar(agora)
		e.erros4xx++
	case status >= 500:
		e.erros5xx++
	case status >= 400:
		e.erros4xx++
	}
}

type estatSSE struct {
	aberturas  contadorJanela
	curtas     contadorJanela
	encerradas uint64
	duracao    histograma
}

type conexaoSSE struct {
	app    string
	inicio time.Time
}

type estatOutbox struct {
	resultados map[string]*contadorJanela
	envio      *estatistica
}

type estatSaude struct {
	conectado     bool
	detalhe       string
	verificadoEm  time.Time
	mudouEm       time.Time
	latenciaMs    float64
	verificacoes  uint64
	desconectadas uint64
	latencia      histograma
}

type batimento struct {
	esperado     time.Duration
	ultimoInicio time.Time
	ultimoFim    time.Time
	duracao      time.Duration
	ultimoErro   string
	ultimoErroEm time.Time
	execucoes    uint64
	falhas       uint64
}

// Coletor e seguro para uso concorrente. Mutex unico pelo mesmo motivo do
// metrica.Registro: o trabalho sob o lock e somar inteiros.
type Coletor struct {
	agora  func() time.Time
	inicio time.Time

	emAndamento atomic.Int64

	mu       sync.Mutex
	global   *estatistica
	rotas    map[string]*estatistica
	apps     map[string]*estatistica
	provedor map[string]*estatistica
	// provedorGlobal soma todas as caixas: e a serie do grafico.
	provedorGlobal *estatistica
	sse            map[string]*estatSSE
	sseAtivas      map[uint64]conexaoSSE
	sseProxID      uint64
	sseRecusas     map[string]*contadorJanela
	outbox         map[string]*estatOutbox
	saude          map[string]*estatSaude
	batimentos     map[string]*batimento

	fontes Fontes
	serie  *serie
}

// Fontes sao os componentes que o coletor le na hora do retrato, em vez de
// receber evento a evento. Qualquer um pode ser nil.
type Fontes struct {
	Hub       FonteHub
	Escutador FonteEscutador
	Banco     *Banco
	Metricas  FonteMetricas
	Instancia string
	Versao    string
}

func NovoColetor() *Coletor {
	agora := time.Now()
	return &Coletor{
		agora:          time.Now,
		inicio:         agora,
		global:         novaEstatistica(limitesLatenciaMs),
		rotas:          map[string]*estatistica{},
		apps:           map[string]*estatistica{},
		provedor:       map[string]*estatistica{},
		provedorGlobal: novaEstatistica(limitesLatenciaMs),
		sse:            map[string]*estatSSE{},
		sseAtivas:      map[uint64]conexaoSSE{},
		sseRecusas:     map[string]*contadorJanela{},
		outbox:         map[string]*estatOutbox{},
		saude:          map[string]*estatSaude{},
		batimentos:     map[string]*batimento{},
		serie:          novaSerie(),
	}
}

// ComFontes liga os componentes lidos no retrato. Chamado uma vez, na
// montagem, antes de o servidor atender.
func (c *Coletor) ComFontes(f Fontes) *Coletor {
	c.fontes = f
	return c
}

// rotaNaoRoteada agrupa tudo que nao casou com rota: varredura de robo na
// internet geraria uma serie por path inventado.
const rotaNaoRoteada = "nao_roteada"

// RequisicaoHTTP registra uma requisicao terminada. app vazio = rota sem
// token de aplicacao (webhook, /health, /c/{token}).
func (c *Coletor) RequisicaoHTTP(rota, app string, duracao time.Duration, status int) {
	agora := c.agora()
	ms := float64(duracao.Microseconds()) / 1000

	c.mu.Lock()
	defer c.mu.Unlock()
	c.global.observar(agora, ms, status)
	r, ok := c.rotas[rota]
	if !ok {
		r = novaEstatistica(limitesLatenciaMs)
		c.rotas[rota] = r
	}
	r.observar(agora, ms, status)
	if app != "" {
		a, ok := c.apps[app]
		if !ok {
			a = novaEstatistica(limitesLatenciaMs)
			c.apps[app] = a
		}
		a.observar(agora, ms, status)
	}
}

// SSEConectou registra a abertura e devolve quem fecha. O par fica no
// handler (defer), como o gauge de internal/metrica.
func (c *Coletor) SSEConectou(app string) (encerrar func()) {
	agora := c.agora()

	c.mu.Lock()
	c.sseProxID++
	id := c.sseProxID
	c.sseAtivas[id] = conexaoSSE{app: app, inicio: agora}
	c.sseDe(app).aberturas.somar(agora)
	c.mu.Unlock()

	return func() {
		fim := c.agora()
		c.mu.Lock()
		defer c.mu.Unlock()
		con, ok := c.sseAtivas[id]
		if !ok {
			return
		}
		delete(c.sseAtivas, id)
		s := c.sseDe(con.app)
		s.encerradas++
		vida := fim.Sub(con.inicio)
		s.duracao.observar(vida.Seconds())
		if vida < 30*time.Second {
			s.curtas.somar(fim)
		}
	}
}

// SSERecusada conta o EventSource barrado antes de abrir (token vencido,
// tempo real desligado). Token vencido em laco e o que faz a tela pedir
// token novo ao portal sem parar.
func (c *Coletor) SSERecusada(motivo string) {
	agora := c.agora()
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.sseRecusas[motivo]
	if !ok {
		r = &contadorJanela{}
		c.sseRecusas[motivo] = r
	}
	r.somar(agora)
}

func (c *Coletor) sseDe(app string) *estatSSE {
	s, ok := c.sse[app]
	if !ok {
		s = &estatSSE{duracao: novoHistograma(limitesDuracaoS)}
		c.sse[app] = s
	}
	return s
}

// ChamadaProvedor mede cada ida a z-api (envio, midia, status). Satisfaz
// caixa.ObservadorProvedor.
func (c *Coletor) ChamadaProvedor(caixa, operacao string, duracao time.Duration, err error) {
	agora := c.agora()
	status := 200
	if err != nil {
		status = 500
	}
	chave := caixa + "|" + operacao

	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.provedor[chave]
	if !ok {
		e = novaEstatistica(limitesLatenciaMs)
		c.provedor[chave] = e
	}
	ms := float64(duracao.Microseconds()) / 1000
	e.observar(agora, ms, status)
	c.provedorGlobal.observar(agora, ms, status)
}

// EnvioOutbox registra o desfecho de uma mensagem processada pelo outbox:
// enviada, retentativa, falha ou bloqueada. Satisfaz outbox.Observador.
func (c *Coletor) EnvioOutbox(caixa, resultado string, duracao time.Duration) {
	agora := c.agora()
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.outbox[caixa]
	if !ok {
		o = &estatOutbox{resultados: map[string]*contadorJanela{}, envio: novaEstatistica(limitesLatenciaMs)}
		c.outbox[caixa] = o
	}
	r, ok := o.resultados[resultado]
	if !ok {
		r = &contadorJanela{}
		o.resultados[resultado] = r
	}
	r.somar(agora)
	status := 200
	if resultado == "falha" {
		status = 500
	}
	o.envio.observar(agora, float64(duracao.Microseconds())/1000, status)
}

// SaudeCaixa guarda a ultima verificacao do monitor de saude. Satisfaz
// saude.Observador. Em memoria, para o painel nao ler provedor_saude.
func (c *Coletor) SaudeCaixa(caixa string, conectado bool, latencia time.Duration, detalhe string) {
	agora := c.agora()
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.saude[caixa]
	if !ok {
		s = &estatSaude{latencia: novoHistograma(limitesLatenciaMs), mudouEm: agora, conectado: conectado}
		c.saude[caixa] = s
	}
	if s.conectado != conectado {
		s.mudouEm = agora
	}
	s.conectado = conectado
	s.detalhe = detalhe
	s.verificadoEm = agora
	s.latenciaMs = float64(latencia.Microseconds()) / 1000
	s.latencia.observar(s.latenciaMs)
	s.verificacoes++
	if !conectado {
		s.desconectadas++
	}
}

// EsperarBatimento declara o intervalo de uma rotina de fundo. Sem ele o
// diagnostico nao sabe quando "nao rodou" vira "parou".
func (c *Coletor) EsperarBatimento(nome string, intervalo time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batimentoDe(nome).esperado = intervalo
}

// Batimento registra uma passada de rotina de fundo (ciclo do outbox,
// verificacao de saude).
func (c *Coletor) Batimento(nome string, inicio time.Time, err error) {
	fim := c.agora()
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.batimentoDe(nome)
	b.ultimoInicio = inicio
	b.ultimoFim = fim
	b.duracao = fim.Sub(inicio)
	b.execucoes++
	if err != nil {
		b.falhas++
		b.ultimoErro = err.Error()
		b.ultimoErroEm = fim
	}
}

func (c *Coletor) batimentoDe(nome string) *batimento {
	b, ok := c.batimentos[nome]
	if !ok {
		b = &batimento{}
		c.batimentos[nome] = b
	}
	return b
}
