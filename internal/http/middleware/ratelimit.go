package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// janelaFixa conta ocorrencias por chave numa janela fixa, em memoria.
//
// Compartilhada pelos dois limites que existem -- por IP (endpoints
// publicos, fase 12) e por aplicacao (barramento, fase 9). A mecanica e a
// mesma nos dois; o que muda e de onde sai a chave e de onde sai o teto,
// e e so isso que os dois tipos abaixo acrescentam.
//
// Em memoria e por instancia: duas instancias atras de um proxy dao ao
// chamador o dobro do teto nominal. Assumido -- o limite existe para
// conter laco e abuso, ordens de grandeza acima do teto, nao para medir
// cota com precisao. Um limiter distribuido trocaria isso por uma
// dependencia nova (Redis), que o plano proibe (diretriz 3).
type janelaFixa struct {
	mu       sync.Mutex
	janela   time.Duration
	contador map[string]*contadorJanela
}

type contadorJanela struct {
	inicio time.Time
	total  int
}

func novaJanelaFixa(janela time.Duration) janelaFixa {
	return janelaFixa{janela: janela, contador: make(map[string]*contadorJanela)}
}

// limiteMapaAntesDeLimpar evita que chaves que pararam de aparecer fiquem
// pra sempre no mapa -- sem isso, meses de trafego de clientes distintos
// clicando em /c/{token} vazam memoria lentamente.
const limiteMapaAntesDeLimpar = 10_000

func (j *janelaFixa) permitir(chave string, max int) bool {
	j.mu.Lock()
	defer j.mu.Unlock()

	agora := time.Now()
	c, existe := j.contador[chave]
	if !existe || agora.Sub(c.inicio) >= j.janela {
		if len(j.contador) >= limiteMapaAntesDeLimpar {
			j.limparExpirados(agora)
		}
		j.contador[chave] = &contadorJanela{inicio: agora, total: 1}
		return true
	}
	if c.total >= max {
		return false
	}
	c.total++
	return true
}

func (j *janelaFixa) limparExpirados(agora time.Time) {
	for chave, c := range j.contador {
		if agora.Sub(c.inicio) >= j.janela {
			delete(j.contador, chave)
		}
	}
}

// LimiteRequisicoes limita requisicoes por IP numa janela fixa -- protege
// os endpoints publicos (sem token de aplicacao: webhooks, /disparos,
// /c/{token}) contra abuso (fase 12).
//
// Por IP e o que da para fazer aqui: quem chama esses endpoints nao se
// identifica. Nas rotas autenticadas o limite e por aplicacao
// (LimitePorAplicacao), que e mais justo e mais util -- ver a fase 9.
type LimiteRequisicoes struct {
	janela janelaFixa
	max    int
}

// NovoLimiteRequisicoes cria o limiter. max <= 0 desliga o limite (fail
// open) -- usado quando o operador nao configurou RATE_LIMIT_POR_MINUTO.
func NovoLimiteRequisicoes(max int, janela time.Duration) *LimiteRequisicoes {
	return &LimiteRequisicoes{janela: novaJanelaFixa(janela), max: max}
}

// Middleware aplica o limite por IP do cliente (secao 5: reverse proxy
// termina TLS na frente, entao o IP real vem em X-Forwarded-For).
func (l *LimiteRequisicoes) Middleware(next http.Handler) http.Handler {
	if l.max <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.janela.permitir(clientIP(r), l.max) {
			responder429(w, l.janela.janela)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP prefere X-Forwarded-For (mandado pelo reverse proxy, secao 5) --
// sem ele, todo cliente apareceria com o IP do proxy, e o limite valeria
// pra todo mundo junto em vez de por cliente.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
