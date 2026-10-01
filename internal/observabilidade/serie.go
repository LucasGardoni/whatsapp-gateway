package observabilidade

import (
	"context"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"
)

// IntervaloSerie e o passo do amostrador; pontosSerie cobre duas horas.
// Em memoria: 480 structs pequenas, e o painel nao precisa de banco para
// desenhar a ultima manha.
const (
	IntervaloSerie = 15 * time.Second
	pontosSerie    = 480
)

// Ponto e uma amostra da serie. Taxas sao por minuto, calculadas pela
// diferenca dos cumulativos entre duas amostras.
type Ponto struct {
	T              time.Time `json:"t"`
	Requisicoes    float64   `json:"req_min"`
	Erros5xx       float64   `json:"erros_5xx_min"`
	P95Ms          float64   `json:"p95_ms"`
	EmAndamento    int64     `json:"em_andamento"`
	SSEAbertas     int       `json:"sse_abertas"`
	SSEAberturas   float64   `json:"sse_aberturas_min"`
	Eventos        float64   `json:"eventos_min"`
	Descartados    uint64    `json:"eventos_descartados"`
	AtrasoMs       float64   `json:"atraso_tempo_real_ms"`
	Goroutines     int       `json:"goroutines"`
	HeapMB         float64   `json:"heap_mb"`
	CPUPct         float64   `json:"cpu_pct"`
	PoolEmUso      int32     `json:"pool_em_uso"`
	PoolMax        int32     `json:"pool_max"`
	PoolEsperas    int64     `json:"pool_esperas"`
	BancoConexoes  int64     `json:"banco_conexoes"`
	FilaPendentes  int64     `json:"fila_pendentes"`
	ProvedorP95Ms  float64   `json:"provedor_p95_ms"`
	ProvedorFalhas float64   `json:"provedor_falhas_min"`
}

type anterior struct {
	valido      bool
	em          time.Time
	global      histograma
	erros5xx    uint64
	provedor    histograma
	provErros   uint64
	aberturas   uint64
	entregues   uint64
	descartados uint64
	esperas     int64
	cpu         float64
	atrasoTotal float64
	entregas    uint64
}

type serie struct {
	mu       sync.Mutex
	pontos   []Ponto
	anterior anterior
}

func novaSerie() *serie { return &serie{} }

func (s *serie) ultimo() (Ponto, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pontos) == 0 {
		return Ponto{}, false
	}
	return s.pontos[len(s.pontos)-1], true
}

func (s *serie) ultimoCPU() float64 {
	p, _ := s.ultimo()
	return p.CPUPct
}

func (s *serie) ultimoAtraso() float64 {
	p, _ := s.ultimo()
	return p.AtrasoMs
}

func (s *serie) esperasRecentes(agora time.Time, minutos int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	corte := agora.Add(-time.Duration(minutos) * time.Minute)
	var soma int64
	for i := len(s.pontos) - 1; i >= 0 && s.pontos[i].T.After(corte); i-- {
		soma += s.pontos[i].PoolEsperas
	}
	return soma
}

func (s *serie) descartadosRecentes(agora time.Time, minutos int) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	corte := agora.Add(-time.Duration(minutos) * time.Minute)
	var soma uint64
	for i := len(s.pontos) - 1; i >= 0 && s.pontos[i].T.After(corte); i-- {
		soma += s.pontos[i].Descartados
	}
	return soma
}

// Serie devolve os pontos dos ultimos `minutos`.
func (c *Coletor) Serie(minutos int) []Ponto {
	corte := c.agora().Add(-time.Duration(minutos) * time.Minute)
	c.serie.mu.Lock()
	defer c.serie.mu.Unlock()
	saida := make([]Ponto, 0, len(c.serie.pontos))
	for _, p := range c.serie.pontos {
		if p.T.After(corte) {
			saida = append(saida, p)
		}
	}
	return saida
}

// Executar amostra a cada IntervaloSerie ate o contexto acabar. Tambem
// mantem o cache do banco quente, para o painel nunca pagar a consulta.
func (c *Coletor) Executar(ctx context.Context) error {
	c.amostrar(ctx)
	ticker := time.NewTicker(IntervaloSerie)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.amostrar(ctx)
		}
	}
}

var nomesCPU = []string{
	"/cpu/classes/user:cpu-seconds",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/scavenge/total:cpu-seconds",
}

// cpuSegundos e a estimativa do runtime do tempo de CPU gasto pelo
// processo. Portavel (o gateway roda como servico Windows), sem syscall.
func cpuSegundos() float64 {
	amostras := make([]metrics.Sample, len(nomesCPU))
	for i, n := range nomesCPU {
		amostras[i].Name = n
	}
	metrics.Read(amostras)
	var total float64
	for _, a := range amostras {
		if a.Value.Kind() == metrics.KindFloat64 {
			total += a.Value.Float64()
		}
	}
	return total
}

func (c *Coletor) amostrar(ctx context.Context) {
	if c.fontes.Banco != nil {
		c.fontes.Banco.Atualizar(ctx)
	}

	agora := c.agora()
	atual := anterior{valido: true, em: agora, cpu: cpuSegundos()}

	c.mu.Lock()
	atual.global = c.global.total.copia()
	atual.erros5xx = c.global.erros5xx
	atual.provedor = c.provedorGlobal.total.copia()
	atual.provErros = c.provedorGlobal.erros5xx
	for _, e := range c.sse {
		atual.aberturas += e.aberturas.total
	}
	sseAbertas := len(c.sseAtivas)
	c.mu.Unlock()

	if c.fontes.Hub != nil {
		for _, h := range c.fontes.Hub.Estatisticas() {
			atual.entregues += h.Entregues
			atual.descartados += h.Descartados
		}
	}
	if c.fontes.Escutador != nil {
		e := c.fontes.Escutador.Estado()
		atual.atrasoTotal, atual.entregas = e.AtrasoTotalMs, e.Entregas
	}
	pool := c.fontes.Banco.Pool()
	atual.esperas = pool.Esperas

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	p := Ponto{
		T:           agora,
		EmAndamento: c.emAndamento.Load(),
		SSEAbertas:  sseAbertas,
		Goroutines:  runtime.NumGoroutine(),
		HeapMB:      float64(mem.HeapAlloc) / (1 << 20),
		PoolEmUso:   pool.EmUso,
		PoolMax:     pool.Max,
	}
	if c.fontes.Banco != nil {
		b := c.fontes.Banco.Retrato(ctx)
		p.BancoConexoes = b.Atividade.DesteBanco
		for _, f := range b.Fila {
			p.FilaPendentes += f.Pendentes
		}
	}

	c.serie.mu.Lock()
	defer c.serie.mu.Unlock()
	ant := c.serie.anterior
	if ant.valido {
		minutos := agora.Sub(ant.em).Minutes()
		if minutos <= 0 {
			minutos = IntervaloSerie.Minutes()
		}
		porMinuto := func(atual, anterior uint64) float64 {
			if atual < anterior {
				return 0
			}
			return float64(atual-anterior) / minutos
		}
		d := atual.global.diferenca(&ant.global)
		p.Requisicoes = float64(d.n) / minutos
		p.P95Ms = d.percentil(0.95)
		p.Erros5xx = porMinuto(atual.erros5xx, ant.erros5xx)
		dp := atual.provedor.diferenca(&ant.provedor)
		p.ProvedorP95Ms = dp.percentil(0.95)
		p.ProvedorFalhas = porMinuto(atual.provErros, ant.provErros)
		p.SSEAberturas = porMinuto(atual.aberturas, ant.aberturas)
		p.Eventos = porMinuto(atual.entregues, ant.entregues)
		if atual.descartados >= ant.descartados {
			p.Descartados = atual.descartados - ant.descartados
		}
		if atual.esperas >= ant.esperas {
			p.PoolEsperas = atual.esperas - ant.esperas
		}
		if atual.entregas > ant.entregas {
			p.AtrasoMs = (atual.atrasoTotal - ant.atrasoTotal) / float64(atual.entregas-ant.entregas)
		}
		if parede := agora.Sub(ant.em).Seconds(); parede > 0 {
			p.CPUPct = 100 * (atual.cpu - ant.cpu) / (parede * float64(runtime.GOMAXPROCS(0)))
			if p.CPUPct < 0 {
				p.CPUPct = 0
			}
		}
	}
	c.serie.anterior = atual
	c.serie.pontos = append(c.serie.pontos, p)
	if len(c.serie.pontos) > pontosSerie {
		c.serie.pontos = append(c.serie.pontos[:0:0], c.serie.pontos[len(c.serie.pontos)-pontosSerie:]...)
	}
}
