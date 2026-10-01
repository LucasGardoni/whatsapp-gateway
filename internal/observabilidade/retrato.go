package observabilidade

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/eventos"
	"github.com/LucasGardoni/whatsapp-gateway/internal/metrica"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
)

type FonteHub interface {
	Estatisticas() []sse.EstatisticaApp
}

type FonteEscutador interface {
	Estado() eventos.Estado
}

type FonteMetricas interface {
	Amostrar() []metrica.Amostra
}

// minutosRecentes e a janela de "agora" do painel: curta para reagir,
// longa o bastante para um percentil ter amostra.
const minutosRecentes = 5

type Retrato struct {
	GeradoEm   time.Time          `json:"gerado_em"`
	Instancia  Instancia          `json:"instancia"`
	Status     string             `json:"status"`
	Alertas    []Alerta           `json:"alertas"`
	Processo   RetratoProcesso    `json:"processo"`
	HTTP       RetratoHTTP        `json:"http"`
	Aplicacoes []RetratoAplicacao `json:"aplicacoes"`
	SSE        RetratoSSE         `json:"sse"`
	TempoReal  *RetratoTempoReal  `json:"tempo_real"`
	Caixas     []RetratoCaixa     `json:"caixas"`
	Provedor   []RetratoProvedor  `json:"provedor"`
	Pool       RetratoPool        `json:"pool"`
	Banco      RetratoBanco       `json:"banco"`
	Rotinas    []RetratoRotina    `json:"rotinas"`
}

type Instancia struct {
	ID         string    `json:"id"`
	Versao     string    `json:"versao"`
	GoVersao   string    `json:"go_versao"`
	IniciadoEm time.Time `json:"iniciado_em"`
	UptimeS    float64   `json:"uptime_s"`
}

// Niveis de alerta, do pior para o melhor.
const (
	NivelCritico = "critico"
	NivelAtencao = "atencao"
	NivelOK      = "ok"
)

type Alerta struct {
	Nivel    string `json:"nivel"`
	Area     string `json:"area"`
	Mensagem string `json:"mensagem"`
}

type RetratoProcesso struct {
	Goroutines      int     `json:"goroutines"`
	HeapMB          float64 `json:"heap_mb"`
	SistemaMB       float64 `json:"sistema_mb"`
	GCs             uint32  `json:"gcs"`
	PausaGCTotalMs  float64 `json:"pausa_gc_total_ms"`
	UltimaPausaGCMs float64 `json:"ultima_pausa_gc_ms"`
	CPUs            int     `json:"cpus"`
	GOMAXPROCS      int     `json:"gomaxprocs"`
	CPUPct          float64 `json:"cpu_pct"`
	EmAndamento     int64   `json:"requisicoes_em_andamento"`
}

// Latencia e a janela recente (minutosRecentes) de uma serie.
type Latencia struct {
	Requisicoes uint64  `json:"requisicoes"`
	PorMinuto   float64 `json:"por_minuto"`
	Erros5xx    uint64  `json:"erros_5xx"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
	MediaMs     float64 `json:"media_ms"`
	MaxMs       float64 `json:"max_ms"`
}

type RetratoRota struct {
	Rota      string   `json:"rota"`
	Recente   Latencia `json:"recente"`
	Total     uint64   `json:"total"`
	Erros4xx  uint64   `json:"erros_4xx"`
	Erros5xx  uint64   `json:"erros_5xx"`
	Limitadas uint64   `json:"limitadas_429"`
	// LimitadasRecentes sao os 429 da janela recente.
	LimitadasRecentes uint64  `json:"limitadas_429_recentes"`
	P95Ms             float64 `json:"p95_total_ms"`
}

type RetratoHTTP struct {
	JanelaMinutos int           `json:"janela_minutos"`
	Global        RetratoRota   `json:"global"`
	Rotas         []RetratoRota `json:"rotas"`
}

type RetratoAplicacao struct {
	RetratoRota
	Aplicacao string `json:"aplicacao"`
	// RequisicoesUltimoMinuto e a janela deslizante de 60s de
	// internal/metrica -- o mesmo numero que o rate limit enxerga.
	RequisicoesUltimoMinuto int64            `json:"requisicoes_ultimo_minuto"`
	MensagensUltimoMinuto   map[string]int64 `json:"mensagens_ultimo_minuto"`
	SSEAbertas              int64            `json:"sse_abertas"`
}

type RetratoSSE struct {
	Abertas     int     `json:"abertas"`
	MaisAntigaS float64 `json:"mais_antiga_s"`
	// DescartadosRecentes vem da serie, somando todas as aplicacoes.
	DescartadosRecentes uint64             `json:"descartados_recentes"`
	Aplicacoes          []RetratoSSEApp    `json:"aplicacoes"`
	Recusas             []RetratoSSERecusa `json:"recusas"`
}

type RetratoSSEApp struct {
	Aplicacao          string  `json:"aplicacao"`
	Abertas            int     `json:"abertas"`
	Destinos           int     `json:"destinos"`
	MaiorPorDestino    int     `json:"maior_por_destino"`
	DestinosComVarias  int     `json:"destinos_com_varias"`
	AberturasRecentes  uint64  `json:"aberturas_recentes"`
	AberturasPorMinuto float64 `json:"aberturas_por_minuto"`
	CurtasRecentes     uint64  `json:"curtas_recentes"`
	AberturasTotal     uint64  `json:"aberturas_total"`
	Encerradas         uint64  `json:"encerradas"`
	DuracaoMedianaS    float64 `json:"duracao_mediana_s"`
	EventosEntregues   uint64  `json:"eventos_entregues"`
	EventosDescartados uint64  `json:"eventos_descartados"`
}

type RetratoSSERecusa struct {
	Motivo   string `json:"motivo"`
	Recentes uint64 `json:"recentes"`
	Total    uint64 `json:"total"`
}

type RetratoTempoReal struct {
	eventos.Estado
	AtrasoMedioMs float64 `json:"atraso_medio_ms"`
}

type RetratoCaixa struct {
	Caixa              string                     `json:"caixa"`
	Conectado          *bool                      `json:"conectado"`
	Detalhe            string                     `json:"detalhe,omitempty"`
	VerificadoEm       *time.Time                 `json:"verificado_em"`
	EstadoDesde        *time.Time                 `json:"estado_desde"`
	LatenciaMs         float64                    `json:"latencia_ms"`
	DisponibilidadePct float64                    `json:"disponibilidade_pct"`
	Fila               *RetratoFila               `json:"fila"`
	Envios             map[string]RetratoContagem `json:"envios"`
	EnvioP95Ms         float64                    `json:"envio_p95_ms"`
}

type RetratoContagem struct {
	Recentes uint64 `json:"recentes"`
	Total    uint64 `json:"total"`
}

type RetratoProvedor struct {
	Caixa    string   `json:"caixa"`
	Operacao string   `json:"operacao"`
	Recente  Latencia `json:"recente"`
	Total    uint64   `json:"total"`
	Falhas   uint64   `json:"falhas"`
}

type RetratoRotina struct {
	Nome         string     `json:"nome"`
	EsperadoS    float64    `json:"esperado_s"`
	UltimoFim    *time.Time `json:"ultimo_fim"`
	AtrasoS      float64    `json:"atraso_s"`
	DuracaoMs    float64    `json:"duracao_ms"`
	Execucoes    uint64     `json:"execucoes"`
	Falhas       uint64     `json:"falhas"`
	UltimoErro   string     `json:"ultimo_erro,omitempty"`
	UltimoErroEm *time.Time `json:"ultimo_erro_em,omitempty"`
	Parada       bool       `json:"parada"`
}

type RetratoPool struct {
	Max               int32   `json:"max"`
	Total             int32   `json:"total"`
	EmUso             int32   `json:"em_uso"`
	Ociosas           int32   `json:"ociosas"`
	Abrindo           int32   `json:"abrindo"`
	Aquisicoes        int64   `json:"aquisicoes"`
	AquisicaoMediaMs  float64 `json:"aquisicao_media_ms"`
	Esperas           int64   `json:"esperas"`
	EsperaTotalMs     float64 `json:"espera_total_ms"`
	Canceladas        int64   `json:"canceladas"`
	NovasConexoes     int64   `json:"novas_conexoes"`
	FechadasPorIdade  int64   `json:"fechadas_por_idade"`
	FechadasPorOciosa int64   `json:"fechadas_por_ociosa"`
	// EsperasRecentes vem da serie: aquisicoes que acharam o pool cheio
	// nos ultimos minutos. E o sinal de pool pequeno para a carga.
	EsperasRecentes int64 `json:"esperas_recentes"`
}

type RetratoBanco struct {
	AtualizadoEm time.Time        `json:"atualizado_em"`
	ConsultaMs   float64          `json:"consulta_ms"`
	Erro         string           `json:"erro,omitempty"`
	Servidor     RetratoServidor  `json:"servidor"`
	Atividade    RetratoAtividade `json:"atividade"`
	Fila         []RetratoFila    `json:"fila"`
	Tabelas      []RetratoTabela  `json:"tabelas"`
}

type RetratoServidor struct {
	Versao       string  `json:"versao"`
	MaxConexoes  int64   `json:"max_conexoes"`
	TamanhoBytes int64   `json:"tamanho_bytes"`
	Commits      int64   `json:"commits"`
	Rollbacks    int64   `json:"rollbacks"`
	Deadlocks    int64   `json:"deadlocks"`
	CacheHitPct  float64 `json:"cache_hit_pct"`
	TempBytes    int64   `json:"temp_bytes"`
}

type RetratoAtividade struct {
	// Total e o servidor inteiro (todas as bases): e contra ele que
	// max_connections vale.
	Total              int64   `json:"total_servidor"`
	DesteBanco         int64   `json:"deste_banco"`
	Ativas             int64   `json:"ativas"`
	Ociosas            int64   `json:"ociosas"`
	OciosasEmTransacao int64   `json:"ociosas_em_transacao"`
	AguardandoLock     int64   `json:"aguardando_lock"`
	MaiorConsultaS     float64 `json:"maior_consulta_s"`
	MaiorTransacaoS    float64 `json:"maior_transacao_s"`
}

type RetratoFila struct {
	Caixa         string  `json:"caixa"`
	Pendentes     int64   `json:"pendentes"`
	Enviando      int64   `json:"enviando"`
	MaisAntigaS   float64 `json:"mais_antiga_s"`
	Falhas24h     int64   `json:"falhas_24h"`
	Bloqueadas24h int64   `json:"bloqueadas_24h"`
}

type RetratoTabela struct {
	Nome         string `json:"nome"`
	Linhas       int64  `json:"linhas"`
	Bytes        int64  `json:"bytes"`
	LinhasMortas int64  `json:"linhas_mortas"`
	SeqScans     int64  `json:"seq_scans"`
	IdxScans     int64  `json:"idx_scans"`
}

func latencia(j *janelaHist, agora time.Time) Latencia {
	h, erros := j.ultimos(agora, minutosRecentes)
	return Latencia{
		Requisicoes: h.n,
		PorMinuto:   float64(h.n) / minutosRecentes,
		Erros5xx:    erros,
		P50Ms:       h.percentil(0.50),
		P95Ms:       h.percentil(0.95),
		P99Ms:       h.percentil(0.99),
		MediaMs:     h.media(),
		MaxMs:       h.max,
	}
}

func retratoRota(nome string, e *estatistica, agora time.Time) RetratoRota {
	return RetratoRota{
		Rota:              nome,
		Recente:           latencia(e.janela, agora),
		Total:             e.total.n,
		Erros4xx:          e.erros4xx,
		Erros5xx:          e.erros5xx,
		Limitadas:         e.limitadas,
		LimitadasRecentes: e.limitadasJanela.ultimos(agora, minutosRecentes),
		P95Ms:             e.total.percentil(0.95),
	}
}

// Retrato monta a fotografia completa. As fontes externas (hub, escutador,
// banco) sao lidas fora do lock do coletor.
func (c *Coletor) Retrato(ctx context.Context) Retrato {
	agora := c.agora()
	r := Retrato{
		GeradoEm: agora,
		Instancia: Instancia{
			ID:         c.fontes.Instancia,
			Versao:     c.fontes.Versao,
			GoVersao:   runtime.Version(),
			IniciadoEm: c.inicio,
			UptimeS:    agora.Sub(c.inicio).Seconds(),
		},
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	r.Processo = RetratoProcesso{
		Goroutines:      runtime.NumGoroutine(),
		HeapMB:          float64(mem.HeapAlloc) / (1 << 20),
		SistemaMB:       float64(mem.Sys) / (1 << 20),
		GCs:             mem.NumGC,
		PausaGCTotalMs:  float64(mem.PauseTotalNs) / 1e6,
		UltimaPausaGCMs: float64(mem.PauseNs[(mem.NumGC+255)%256]) / 1e6,
		CPUs:            runtime.NumCPU(),
		GOMAXPROCS:      runtime.GOMAXPROCS(0),
		CPUPct:          c.serie.ultimoCPU(),
		EmAndamento:     c.emAndamento.Load(),
	}

	c.retratoInterno(&r, agora)

	var hub []sse.EstatisticaApp
	if c.fontes.Hub != nil {
		hub = c.fontes.Hub.Estatisticas()
	}
	c.mesclarHub(&r, hub)
	r.SSE.DescartadosRecentes = c.serie.descartadosRecentes(agora, minutosRecentes)

	var amostras []metrica.Amostra
	if c.fontes.Metricas != nil {
		amostras = c.fontes.Metricas.Amostrar()
	}
	mesclarMetricas(&r, amostras)

	if c.fontes.Escutador != nil {
		r.TempoReal = &RetratoTempoReal{Estado: c.fontes.Escutador.Estado(), AtrasoMedioMs: c.serie.ultimoAtraso()}
	}

	r.Pool = c.fontes.Banco.Pool()
	r.Pool.EsperasRecentes = c.serie.esperasRecentes(agora, minutosRecentes)
	r.Banco = c.fontes.Banco.Retrato(ctx)
	mesclarFila(&r)

	r.Alertas = diagnosticar(&r)
	r.Status = NivelOK
	for _, a := range r.Alertas {
		if a.Nivel == NivelCritico {
			r.Status = NivelCritico
			break
		}
		r.Status = NivelAtencao
	}
	return r
}

func (c *Coletor) retratoInterno(r *Retrato, agora time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	r.HTTP = RetratoHTTP{JanelaMinutos: minutosRecentes, Global: retratoRota("todas", c.global, agora)}
	for nome, e := range c.rotas {
		r.HTTP.Rotas = append(r.HTTP.Rotas, retratoRota(nome, e, agora))
	}
	sort.Slice(r.HTTP.Rotas, func(i, k int) bool {
		a, b := r.HTTP.Rotas[i], r.HTTP.Rotas[k]
		if a.Recente.Requisicoes != b.Recente.Requisicoes {
			return a.Recente.Requisicoes > b.Recente.Requisicoes
		}
		return a.Total > b.Total
	})

	for nome, e := range c.apps {
		r.Aplicacoes = append(r.Aplicacoes, RetratoAplicacao{Aplicacao: nome, RetratoRota: retratoRota(nome, e, agora)})
	}

	// SSE: as conexoes vivas sao do coletor, o resto vem do hub depois.
	porApp := map[string]*RetratoSSEApp{}
	sseDe := func(app string) *RetratoSSEApp {
		s, ok := porApp[app]
		if !ok {
			s = &RetratoSSEApp{Aplicacao: app}
			porApp[app] = s
		}
		return s
	}
	for _, con := range c.sseAtivas {
		sseDe(con.app).Abertas++
		r.SSE.Abertas++
		if idade := agora.Sub(con.inicio).Seconds(); idade > r.SSE.MaisAntigaS {
			r.SSE.MaisAntigaS = idade
		}
	}
	for app, e := range c.sse {
		s := sseDe(app)
		s.AberturasRecentes = e.aberturas.ultimos(agora, minutosRecentes)
		s.AberturasPorMinuto = float64(s.AberturasRecentes) / minutosRecentes
		s.CurtasRecentes = e.curtas.ultimos(agora, minutosRecentes)
		s.AberturasTotal = e.aberturas.total
		s.Encerradas = e.encerradas
		s.DuracaoMedianaS = e.duracao.percentil(0.5)
	}
	for _, s := range porApp {
		r.SSE.Aplicacoes = append(r.SSE.Aplicacoes, *s)
	}
	for motivo, cj := range c.sseRecusas {
		r.SSE.Recusas = append(r.SSE.Recusas, RetratoSSERecusa{Motivo: motivo, Recentes: cj.ultimos(agora, minutosRecentes), Total: cj.total})
	}
	sort.Slice(r.SSE.Recusas, func(i, k int) bool { return r.SSE.Recusas[i].Motivo < r.SSE.Recusas[k].Motivo })

	caixas := map[string]*RetratoCaixa{}
	caixaDe := func(codigo string) *RetratoCaixa {
		cx, ok := caixas[codigo]
		if !ok {
			cx = &RetratoCaixa{Caixa: codigo, Envios: map[string]RetratoContagem{}}
			caixas[codigo] = cx
		}
		return cx
	}
	for codigo, s := range c.saude {
		cx := caixaDe(codigo)
		conectado := s.conectado
		verificado, desde := s.verificadoEm, s.mudouEm
		cx.Conectado = &conectado
		cx.Detalhe = s.detalhe
		cx.VerificadoEm = &verificado
		cx.EstadoDesde = &desde
		cx.LatenciaMs = s.latenciaMs
		if s.verificacoes > 0 {
			cx.DisponibilidadePct = 100 * float64(s.verificacoes-s.desconectadas) / float64(s.verificacoes)
		}
	}
	for codigo, o := range c.outbox {
		cx := caixaDe(codigo)
		for resultado, cj := range o.resultados {
			cx.Envios[resultado] = RetratoContagem{Recentes: cj.ultimos(agora, minutosRecentes), Total: cj.total}
		}
		h, _ := o.envio.janela.ultimos(agora, minutosRecentes)
		cx.EnvioP95Ms = h.percentil(0.95)
	}
	for _, cx := range caixas {
		r.Caixas = append(r.Caixas, *cx)
	}

	for chave, e := range c.provedor {
		caixa, operacao := chave, ""
		for i := 0; i < len(chave); i++ {
			if chave[i] == '|' {
				caixa, operacao = chave[:i], chave[i+1:]
				break
			}
		}
		r.Provedor = append(r.Provedor, RetratoProvedor{
			Caixa: caixa, Operacao: operacao,
			Recente: latencia(e.janela, agora),
			Total:   e.total.n,
			Falhas:  e.erros5xx,
		})
	}
	sort.Slice(r.Provedor, func(i, k int) bool {
		if r.Provedor[i].Caixa != r.Provedor[k].Caixa {
			return r.Provedor[i].Caixa < r.Provedor[k].Caixa
		}
		return r.Provedor[i].Operacao < r.Provedor[k].Operacao
	})

	for nome, b := range c.batimentos {
		rot := RetratoRotina{
			Nome:      nome,
			EsperadoS: b.esperado.Seconds(),
			DuracaoMs: float64(b.duracao.Microseconds()) / 1000,
			Execucoes: b.execucoes,
			Falhas:    b.falhas,
		}
		referencia := b.ultimoFim
		if referencia.IsZero() {
			referencia = c.inicio
		} else {
			fim := b.ultimoFim
			rot.UltimoFim = &fim
		}
		rot.AtrasoS = agora.Sub(referencia).Seconds()
		if b.ultimoErro != "" {
			em := b.ultimoErroEm
			rot.UltimoErro = b.ultimoErro
			rot.UltimoErroEm = &em
		}
		// tres intervalos sem batimento e a rotina travada, nao lenta.
		if b.esperado > 0 && agora.Sub(referencia) > 3*b.esperado+10*time.Second {
			rot.Parada = true
		}
		r.Rotinas = append(r.Rotinas, rot)
	}
	sort.Slice(r.Rotinas, func(i, k int) bool { return r.Rotinas[i].Nome < r.Rotinas[k].Nome })
}

func (c *Coletor) mesclarHub(r *Retrato, hub []sse.EstatisticaApp) {
	indice := map[string]int{}
	for i, s := range r.SSE.Aplicacoes {
		indice[s.Aplicacao] = i
	}
	for _, h := range hub {
		i, ok := indice[h.Aplicacao]
		if !ok {
			r.SSE.Aplicacoes = append(r.SSE.Aplicacoes, RetratoSSEApp{Aplicacao: h.Aplicacao})
			i = len(r.SSE.Aplicacoes) - 1
			indice[h.Aplicacao] = i
		}
		s := &r.SSE.Aplicacoes[i]
		s.Destinos = h.Destinos
		s.MaiorPorDestino = h.MaiorPorDestino
		s.DestinosComVarias = h.DestinosComVarias
		s.EventosEntregues = h.Entregues
		s.EventosDescartados = h.Descartados
	}
	sort.Slice(r.SSE.Aplicacoes, func(i, k int) bool { return r.SSE.Aplicacoes[i].Aplicacao < r.SSE.Aplicacoes[k].Aplicacao })
}

func mesclarMetricas(r *Retrato, amostras []metrica.Amostra) {
	indice := map[string]int{}
	for i, a := range r.Aplicacoes {
		indice[a.Aplicacao] = i
	}
	for _, m := range amostras {
		i, ok := indice[m.Aplicacao]
		if !ok {
			r.Aplicacoes = append(r.Aplicacoes, RetratoAplicacao{Aplicacao: m.Aplicacao, RetratoRota: RetratoRota{Rota: m.Aplicacao}})
			i = len(r.Aplicacoes) - 1
		}
		a := &r.Aplicacoes[i]
		a.RequisicoesUltimoMinuto = m.RequisicoesPorMinuto
		a.SSEAbertas = m.SSEAbertas
		a.MensagensUltimoMinuto = map[string]int64{}
		for _, canal := range m.Mensagens {
			a.MensagensUltimoMinuto[canal.Canal] = canal.PorMinuto
		}
	}
	sort.Slice(r.Aplicacoes, func(i, k int) bool { return r.Aplicacoes[i].Aplicacao < r.Aplicacoes[k].Aplicacao })
}

// mesclarFila junta a fila do banco a caixa. Caixa so no banco (nunca
// verificada por esta instancia) entra mesmo assim.
func mesclarFila(r *Retrato) {
	for _, f := range r.Banco.Fila {
		f := f
		achou := false
		for i := range r.Caixas {
			if r.Caixas[i].Caixa == f.Caixa {
				r.Caixas[i].Fila = &f
				achou = true
				break
			}
		}
		if !achou {
			r.Caixas = append(r.Caixas, RetratoCaixa{Caixa: f.Caixa, Fila: &f, Envios: map[string]RetratoContagem{}})
		}
	}
	sort.Slice(r.Caixas, func(i, k int) bool { return r.Caixas[i].Caixa < r.Caixas[k].Caixa })
}

// Limiares do diagnostico. Sao de operacao, nao de negocio: o ponto em que
// vale alguem olhar, nao um SLA.
const (
	limiteP95Ms               = 1500
	limiteErros5xxRecentes    = 10
	limiteFilaAtencaoS        = 120
	limiteFilaCriticoS        = 600
	limitePoolUsoPct          = 85
	limiteConexoesBancoPct    = 80
	limiteTransacaoOciosaS    = 60
	limiteConsultaLongaS      = 30
	limiteGoroutines          = 5000
	limiteAberturasPorConexao = 3
	limiteRecusasSSERecentes  = 30
	limiteProvedorP95Ms       = 5000
	limiteAtrasoTempoRealMs   = 5000
)

func diagnosticar(r *Retrato) []Alerta {
	var alertas []Alerta
	add := func(nivel, area, formato string, args ...any) {
		alertas = append(alertas, Alerta{Nivel: nivel, Area: area, Mensagem: fmt.Sprintf(formato, args...)})
	}

	for _, cx := range r.Caixas {
		if cx.Conectado != nil && !*cx.Conectado {
			detalhe := ""
			if cx.Detalhe != "" {
				detalhe = " (" + cx.Detalhe + ")"
			}
			add(NivelCritico, "caixa", "Caixa %s desconectada do WhatsApp%s: a fila dela nao sai.", cx.Caixa, detalhe)
		}
		if cx.Fila != nil {
			switch {
			case cx.Fila.MaisAntigaS >= limiteFilaCriticoS:
				add(NivelCritico, "fila", "Caixa %s tem mensagem pendente ha %s (%d na fila).", cx.Caixa, duracao(cx.Fila.MaisAntigaS), cx.Fila.Pendentes)
			case cx.Fila.MaisAntigaS >= limiteFilaAtencaoS:
				add(NivelAtencao, "fila", "Caixa %s tem mensagem pendente ha %s (%d na fila).", cx.Caixa, duracao(cx.Fila.MaisAntigaS), cx.Fila.Pendentes)
			}
			if cx.Fila.Falhas24h > 0 {
				add(NivelAtencao, "fila", "Caixa %s teve %d mensagem(ns) com falha definitiva nas ultimas 24h.", cx.Caixa, cx.Fila.Falhas24h)
			}
		}
	}

	if r.TempoReal != nil {
		if !r.TempoReal.Conectado {
			add(NivelCritico, "tempo_real", "Conexao LISTEN do tempo real caida: eventos so saem pela varredura (ate 60s de atraso).")
		} else if r.TempoReal.AtrasoMedioMs > limiteAtrasoTempoRealMs {
			add(NivelAtencao, "tempo_real", "Eventos chegando a tela com %.1fs de atraso medio.", r.TempoReal.AtrasoMedioMs/1000)
		}
	}

	for _, rot := range r.Rotinas {
		if rot.Parada {
			add(NivelCritico, "rotina", "Rotina %s sem executar ha %s (esperado a cada %s).", rot.Nome, duracao(rot.AtrasoS), duracao(rot.EsperadoS))
		}
	}

	if r.Banco.Erro != "" {
		add(NivelCritico, "banco", "Falha ao consultar o banco: %s", r.Banco.Erro)
	}
	if r.Pool.Max > 0 && float64(r.Pool.EmUso)*100/float64(r.Pool.Max) >= limitePoolUsoPct {
		add(NivelAtencao, "pool", "Pool de conexoes com %d de %d em uso.", r.Pool.EmUso, r.Pool.Max)
	}
	// o pgxpool conta como espera toda aquisicao sem conexao ociosa, ate a
	// que so abriu conexao nova (subida, pico curto). So e falta de conexao
	// quando o pool ja chegou ao teto.
	if r.Pool.EsperasRecentes > 0 && r.Pool.Max > 0 && r.Pool.Total >= r.Pool.Max {
		add(NivelAtencao, "pool", "%d requisicao(oes) esperaram conexao livre com o pool no teto (%d) nos ultimos %d min.", r.Pool.EsperasRecentes, r.Pool.Max, minutosRecentes)
	}
	if s := r.Banco.Servidor; s.MaxConexoes > 0 && float64(r.Banco.Atividade.Total)*100/float64(s.MaxConexoes) >= limiteConexoesBancoPct {
		add(NivelAtencao, "banco", "Servidor Postgres com %d de %d conexoes (todas as bases).", r.Banco.Atividade.Total, s.MaxConexoes)
	}
	if r.Banco.Atividade.MaiorTransacaoS >= limiteTransacaoOciosaS && r.Banco.Atividade.OciosasEmTransacao > 0 {
		add(NivelAtencao, "banco", "Transacao aberta ha %s com conexao ociosa: segura lock e impede vacuum.", duracao(r.Banco.Atividade.MaiorTransacaoS))
	}
	if r.Banco.Atividade.MaiorConsultaS >= limiteConsultaLongaS {
		add(NivelAtencao, "banco", "Consulta rodando ha %s.", duracao(r.Banco.Atividade.MaiorConsultaS))
	}
	if r.Banco.Atividade.AguardandoLock > 0 {
		add(NivelAtencao, "banco", "%d conexao(oes) aguardando lock.", r.Banco.Atividade.AguardandoLock)
	}

	g := r.HTTP.Global.Recente
	if g.Erros5xx >= limiteErros5xxRecentes {
		add(NivelCritico, "http", "%d respostas 5xx nos ultimos %d min.", g.Erros5xx, minutosRecentes)
	} else if g.Erros5xx > 0 {
		add(NivelAtencao, "http", "%d resposta(s) 5xx nos ultimos %d min.", g.Erros5xx, minutosRecentes)
	}
	if g.Requisicoes >= 20 && g.P95Ms >= limiteP95Ms {
		add(NivelAtencao, "http", "p95 das requisicoes em %.0f ms nos ultimos %d min.", g.P95Ms, minutosRecentes)
	}
	for _, a := range r.Aplicacoes {
		if a.LimitadasRecentes > 0 {
			add(NivelAtencao, "aplicacao", "Aplicacao %s barrada %d vez(es) pelo limite de requisicoes nos ultimos %d min.", a.Aplicacao, a.LimitadasRecentes, minutosRecentes)
		}
	}

	if r.SSE.DescartadosRecentes > 0 {
		add(NivelAtencao, "sse", "%d evento(s) descartado(s) por tela lenta nos ultimos %d min: a tela perde o aviso e so se atualiza ao reconectar.", r.SSE.DescartadosRecentes, minutosRecentes)
	}
	for _, s := range r.SSE.Aplicacoes {
		// reconexao em massa: muito mais aberturas que telas abertas.
		base := s.Abertas
		if base < 5 {
			base = 5
		}
		if s.AberturasRecentes > uint64(base*limiteAberturasPorConexao) {
			add(NivelAtencao, "sse", "%s: %d aberturas de EventSource em %d min para %d conexoes vivas -- telas reconectando em laco.", s.Aplicacao, s.AberturasRecentes, minutosRecentes, s.Abertas)
		}
		if s.DestinosComVarias > 0 {
			add(NivelAtencao, "sse", "%s: %d destino(s) com 3 ou mais conexoes (abas duplicadas, maior = %d).", s.Aplicacao, s.DestinosComVarias, s.MaiorPorDestino)
		}
	}
	for _, rec := range r.SSE.Recusas {
		if rec.Recentes >= limiteRecusasSSERecentes {
			add(NivelAtencao, "sse", "%d EventSource recusados (%s) nos ultimos %d min.", rec.Recentes, rec.Motivo, minutosRecentes)
		}
	}

	for _, p := range r.Provedor {
		if p.Recente.Requisicoes >= 3 && p.Recente.P95Ms >= limiteProvedorP95Ms {
			add(NivelAtencao, "provedor", "Z-API lenta na caixa %s (%s): p95 %.1fs.", p.Caixa, p.Operacao, p.Recente.P95Ms/1000)
		}
		if p.Recente.Erros5xx > 0 && p.Recente.Erros5xx*2 >= p.Recente.Requisicoes {
			add(NivelAtencao, "provedor", "Z-API falhando na caixa %s (%s): %d de %d chamadas.", p.Caixa, p.Operacao, p.Recente.Erros5xx, p.Recente.Requisicoes)
		}
	}

	if r.Processo.Goroutines >= limiteGoroutines {
		add(NivelAtencao, "processo", "%d goroutines vivas: possivel vazamento de conexao.", r.Processo.Goroutines)
	}

	sort.SliceStable(alertas, func(i, k int) bool {
		return alertas[i].Nivel == NivelCritico && alertas[k].Nivel != NivelCritico
	})
	return alertas
}

func duracao(segundos float64) string {
	d := time.Duration(segundos * float64(time.Second))
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dmin", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dmin%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
