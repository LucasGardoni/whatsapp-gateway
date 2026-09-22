// package metrica conta o que cada aplicacao faz no barramento (fase 9 do
// docs/PLANO_BARRAMENTO_MENSAGENS.md).
//
// Tres decisoes que explicam o resto do arquivo:
//
//  1. E EM MEMORIA, e por instancia. Metrica de trafego com um INSERT por
//     requisicao transformaria observabilidade em carga: o barramento
//     escreveria mais linha de contador do que de mensagem. Quem soma as
//     instancias e quem le (um scraper, o painel do CRM), que ja precisa
//     somar de qualquer forma atras de um proxy.
//
//  2. A chave e o CODIGO da aplicacao, e nada mais. Nao ha aqui destino,
//     canal_externo, remetente nem id de mensagem -- so o rotulo do
//     consumidor e contagens. Metrica e o caminho mais facil de vazar o
//     que a secao 2 do plano protege, porque parece inofensiva: um
//     contador por destino diria quem conversa com quem sem nunca ler uma
//     mensagem.
//
//  3. Nao existe ramo por aplicacao. O registro nao sabe que aplicacoes
//     existem -- aprende os codigos que passam por ele (secao 2, item 6).
package metrica

import (
	"sort"
	"sync"
	"time"
)

// baldesPorJanela e o tamanho do anel: 60 baldes de 1s cobrem o "por
// minuto" que a fase 9 pede sem precisar de scraper para derivar taxa.
const baldesPorJanela = 60

// janela e um contador dos ultimos 60 segundos, em baldes de 1s, mais o
// total cumulativo.
//
// Janela deslizante e nao fixa de proposito: com janela fixa, uma rajada
// que cruza a virada do minuto aparece pela metade em dois minutos
// diferentes e nunca como o pico que foi -- que e exatamente o evento
// que se quer ver.
type janela struct {
	baldes [baldesPorJanela]int64
	// ultimoSegundo e o unix seconds do balde mais recente escrito.
	ultimoSegundo int64
	// total nunca zera: e o cumulativo que um scraper prefere, porque
	// sobrevive a ele perder uma raspagem.
	total uint64
}

// avancar zera os baldes que o tempo passou por cima desde a ultima
// escrita. Sem isso, um minuto de silencio seguido de uma requisicao
// leria o anel inteiro de um minuto antigo como se fosse agora.
func (j *janela) avancar(segundo int64) {
	if segundo == j.ultimoSegundo {
		return
	}
	decorrido := segundo - j.ultimoSegundo
	if decorrido < 0 {
		// relogio andou para tras (ajuste de hora, NTP). Descarta a
		// janela inteira em vez de tentar reinterpretar baldes: perder um
		// minuto de taxa e melhor que reportar um pico que nao houve.
		decorrido = baldesPorJanela
	}
	if decorrido > baldesPorJanela {
		decorrido = baldesPorJanela
	}
	for i := int64(1); i <= decorrido; i++ {
		j.baldes[(j.ultimoSegundo+i)%baldesPorJanela] = 0
	}
	j.ultimoSegundo = segundo
}

func (j *janela) somar(agora time.Time) {
	segundo := agora.Unix()
	j.avancar(segundo)
	j.baldes[segundo%baldesPorJanela]++
	j.total++
}

// porMinuto soma o anel. Chama avancar antes para nao contar balde velho
// de quem parou de receber trafego -- ler tem de expirar tanto quanto
// escrever, senao uma aplicacao que ficou quieta continuaria aparecendo
// no ultimo pico dela para sempre.
func (j *janela) porMinuto(agora time.Time) int64 {
	j.avancar(agora.Unix())
	var soma int64
	for _, b := range j.baldes {
		soma += b
	}
	return soma
}

type contadores struct {
	requisicoes janela
	erros       janela
	// mensagens e por canal: uma aplicacao que troca o canal interno pelo
	// whatsapp muda o risco que ela representa (uma tem provedor externo
	// e custo por mensagem, a outra nao), e uma soma unica esconderia isso.
	mensagens map[string]*janela
	// sseAbertas e gauge, nao contador: e quantas conexoes existem AGORA
	// nesta instancia. Soma das instancias = total de telas abertas.
	sseAbertas int64
}

// Registro e seguro para uso concorrente por todo o servidor.
//
// Um mutex unico, e nao um por aplicacao: o trabalho sob o lock e somar
// um inteiro, e dezenas de aplicacoes nao fazem disso contencao. Trocar
// por sharding aqui seria otimizar antes de medir.
type Registro struct {
	mu    sync.Mutex
	agora func() time.Time
	apps  map[string]*contadores
}

func NovoRegistro() *Registro {
	return &Registro{agora: time.Now, apps: make(map[string]*contadores)}
}

func (r *Registro) de(app string) *contadores {
	c, existe := r.apps[app]
	if !existe {
		c = &contadores{mensagens: make(map[string]*janela)}
		r.apps[app] = c
	}
	return c
}

// Requisicao registra uma requisicao autenticada e, se erro, tambem o
// erro.
//
// Os dois no mesmo metodo porque toda requisicao passa por aqui exatamente
// uma vez: com metodos separados, um caminho novo poderia contar o erro e
// esquecer a requisicao, e a taxa de erro passaria de 100%.
func (r *Registro) Requisicao(app string, erro bool) {
	if app == "" {
		return
	}
	agora := r.agora()

	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.de(app)
	c.requisicoes.somar(agora)
	if erro {
		c.erros.somar(agora)
	}
}

// Mensagem registra uma mensagem aceita no barramento, por canal.
func (r *Registro) Mensagem(app, canal string) {
	if app == "" || canal == "" {
		return
	}
	agora := r.agora()

	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.de(app)
	j, existe := c.mensagens[canal]
	if !existe {
		j = &janela{}
		c.mensagens[canal] = j
	}
	j.somar(agora)
}

// SSEAberta soma (+1) ou subtrai (-1) uma conexao EventSource.
//
// Quem chama tem de garantir o par -- no handler e um defer logo depois do
// incremento. Gauge que so sobe e pior que gauge nenhum: ele mentiria
// para sempre, e a mentira cresce com o uso normal do sistema.
func (r *Registro) SSEAberta(app string, delta int64) {
	if app == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.de(app).sseAbertas += delta
}

// AmostraCanal e o par canal/contagem de mensagens.
type AmostraCanal struct {
	Canal     string
	PorMinuto int64
	Total     uint64
}

// Amostra e o retrato de uma aplicacao num instante.
type Amostra struct {
	Aplicacao            string
	RequisicoesPorMinuto int64
	RequisicoesTotal     uint64
	ErrosPorMinuto       int64
	ErrosTotal           uint64
	SSEAbertas           int64
	Mensagens            []AmostraCanal
}

// Amostrar devolve o retrato de todas as aplicacoes, ordenado por codigo
// -- saida estavel para que um diff entre duas leituras mostre o que
// mudou de valor, e nao de posicao.
func (r *Registro) Amostrar() []Amostra {
	agora := r.agora()

	r.mu.Lock()
	defer r.mu.Unlock()

	amostras := make([]Amostra, 0, len(r.apps))
	for app, c := range r.apps {
		a := Amostra{
			Aplicacao:            app,
			RequisicoesPorMinuto: c.requisicoes.porMinuto(agora),
			RequisicoesTotal:     c.requisicoes.total,
			ErrosPorMinuto:       c.erros.porMinuto(agora),
			ErrosTotal:           c.erros.total,
			SSEAbertas:           c.sseAbertas,
			Mensagens:            make([]AmostraCanal, 0, len(c.mensagens)),
		}
		for canal, j := range c.mensagens {
			a.Mensagens = append(a.Mensagens, AmostraCanal{
				Canal:     canal,
				PorMinuto: j.porMinuto(agora),
				Total:     j.total,
			})
		}
		sort.Slice(a.Mensagens, func(i, k int) bool { return a.Mensagens[i].Canal < a.Mensagens[k].Canal })
		amostras = append(amostras, a)
	}
	sort.Slice(amostras, func(i, k int) bool { return amostras[i].Aplicacao < amostras[k].Aplicacao })
	return amostras
}
