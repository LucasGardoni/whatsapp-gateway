package observabilidade

import (
	"math"
	"time"
)

// limitesLatenciaMs cobre de requisicao servida do cache (unidades de ms)
// ate a chamada a z-api que estoura o timeout (dezenas de segundos). Baldes
// fixos, e nao amostras guardadas: memoria constante por rota, e percentil
// estimado e o bastante para dizer "o p95 subiu de 80ms para 2s".
var limitesLatenciaMs = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000}

// limitesDuracaoS e a vida de uma conexao SSE encerrada. Conexao que morre
// em segundos e reconexao em laco; a saudavel dura o expediente.
var limitesDuracaoS = []float64{5, 30, 60, 300, 900, 1800, 3600, 4 * 3600, 12 * 3600}

type histograma struct {
	limites []float64
	// baldes tem um a mais que limites: o ultimo e "acima do maior limite".
	baldes []uint64
	n      uint64
	soma   float64
	max    float64
}

func novoHistograma(limites []float64) histograma {
	return histograma{limites: limites, baldes: make([]uint64, len(limites)+1)}
}

func (h *histograma) observar(v float64) {
	i := 0
	for i < len(h.limites) && v > h.limites[i] {
		i++
	}
	h.baldes[i]++
	h.n++
	h.soma += v
	if v > h.max {
		h.max = v
	}
}

func (h *histograma) somar(o *histograma) {
	for i := range h.baldes {
		h.baldes[i] += o.baldes[i]
	}
	h.n += o.n
	h.soma += o.soma
	if o.max > h.max {
		h.max = o.max
	}
}

// diferenca e o que entrou em h depois de anterior, os dois cumulativos.
// O max nao e subtraivel: fica o do cumulativo, que e teto, nao valor.
func (h *histograma) diferenca(anterior *histograma) histograma {
	d := novoHistograma(h.limites)
	if anterior == nil || len(anterior.baldes) != len(h.baldes) {
		d.somar(h)
		return d
	}
	for i := range h.baldes {
		if h.baldes[i] >= anterior.baldes[i] {
			d.baldes[i] = h.baldes[i] - anterior.baldes[i]
		}
	}
	if h.n >= anterior.n {
		d.n = h.n - anterior.n
	}
	d.soma = math.Max(0, h.soma-anterior.soma)
	d.max = h.max
	return d
}

func (h *histograma) copia() histograma {
	c := novoHistograma(h.limites)
	c.somar(h)
	return c
}

// percentil interpola dentro do balde. O ultimo balde nao tem teto, entao
// usa o maior valor visto -- melhor que inventar um numero.
func (h *histograma) percentil(p float64) float64 {
	if h.n == 0 {
		return 0
	}
	alvo := p * float64(h.n)
	var acumulado float64
	for i, b := range h.baldes {
		if b == 0 {
			continue
		}
		if acumulado+float64(b) >= alvo {
			piso := 0.0
			if i > 0 {
				piso = h.limites[i-1]
			}
			teto := h.max
			if i < len(h.limites) && h.limites[i] < teto {
				teto = h.limites[i]
			}
			if teto < piso {
				teto = piso
			}
			return piso + (teto-piso)*((alvo-acumulado)/float64(b))
		}
		acumulado += float64(b)
	}
	return h.max
}

func (h *histograma) media() float64 {
	if h.n == 0 {
		return 0
	}
	return h.soma / float64(h.n)
}

// minutosJanela e quanto da historia recente fica em baldes por minuto. A
// leitura usa os ultimos cinco; os outros dez sao folga para a janela nao
// ler slot reciclado.
const minutosJanela = 15

// janelaHist e um anel de histogramas por minuto, para "nos ultimos N
// minutos" sem varrer amostra nenhuma.
type janelaHist struct {
	limites []float64
	minuto  [minutosJanela]int64
	hist    [minutosJanela]histograma
	erros   [minutosJanela]uint64
}

func novaJanelaHist(limites []float64) *janelaHist {
	j := &janelaHist{limites: limites}
	for i := range j.hist {
		j.hist[i] = novoHistograma(limites)
	}
	return j
}

func (j *janelaHist) slot(agora time.Time) int {
	m := agora.Unix() / 60
	i := int(m % minutosJanela)
	if j.minuto[i] != m {
		j.minuto[i] = m
		j.hist[i] = novoHistograma(j.limites)
		j.erros[i] = 0
	}
	return i
}

func (j *janelaHist) observar(agora time.Time, v float64, erro bool) {
	i := j.slot(agora)
	j.hist[i].observar(v)
	if erro {
		j.erros[i]++
	}
}

// ultimos soma os n minutos mais recentes, contando o minuto corrente.
func (j *janelaHist) ultimos(agora time.Time, n int) (histograma, uint64) {
	h := novoHistograma(j.limites)
	var erros uint64
	atual := agora.Unix() / 60
	for i := range j.minuto {
		if j.minuto[i] > atual-int64(n) && j.minuto[i] <= atual {
			h.somar(&j.hist[i])
			erros += j.erros[i]
		}
	}
	return h, erros
}

// contadorJanela e o mesmo anel para contagem pura.
type contadorJanela struct {
	minuto [minutosJanela]int64
	valor  [minutosJanela]uint64
	total  uint64
}

func (c *contadorJanela) somar(agora time.Time) {
	m := agora.Unix() / 60
	i := int(m % minutosJanela)
	if c.minuto[i] != m {
		c.minuto[i] = m
		c.valor[i] = 0
	}
	c.valor[i]++
	c.total++
}

func (c *contadorJanela) ultimos(agora time.Time, n int) uint64 {
	atual := agora.Unix() / 60
	var soma uint64
	for i := range c.minuto {
		if c.minuto[i] > atual-int64(n) && c.minuto[i] <= atual {
			soma += c.valor[i]
		}
	}
	return soma
}
