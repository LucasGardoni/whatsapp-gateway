package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// LimiteRequisicoes limita requisicoes por IP numa janela fixa -- protege
// os endpoints publicos (sem token de servico: webhooks, /disparos,
// /c/{token}) contra abuso (fase 12). Em memoria e por janela fixa porque
// a escala e pequena (instancia unica, secao 1) -- nao ha por que trazer
// um limiter distribuido pra isso.
type LimiteRequisicoes struct {
	mu       sync.Mutex
	max      int
	janela   time.Duration
	contador map[string]*contadorJanela
}

type contadorJanela struct {
	inicio time.Time
	total  int
}

// NovoLimiteRequisicoes cria o limiter. max <= 0 desliga o limite (fail
// open) -- usado quando o operador nao configurou RATE_LIMIT_POR_MINUTO.
func NovoLimiteRequisicoes(max int, janela time.Duration) *LimiteRequisicoes {
	return &LimiteRequisicoes{max: max, janela: janela, contador: make(map[string]*contadorJanela)}
}

// Middleware aplica o limite por IP do cliente (secao 5: reverse proxy
// termina TLS na frente, entao o IP real vem em X-Forwarded-For).
func (l *LimiteRequisicoes) Middleware(next http.Handler) http.Handler {
	if l.max <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.permitir(clientIP(r)) {
			http.Error(w, "muitas requisicoes, tente novamente em instantes", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limiteMapaAntesDeLimpar evita que IPs que pararam de aparecer fiquem pra
// sempre no mapa -- sem isso, meses de trafego de clientes distintos
// clicando em /c/{token} vazam memoria lentamente.
const limiteMapaAntesDeLimpar = 10_000

func (l *LimiteRequisicoes) permitir(chave string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	agora := time.Now()
	c, existe := l.contador[chave]
	if !existe || agora.Sub(c.inicio) >= l.janela {
		if len(l.contador) >= limiteMapaAntesDeLimpar {
			l.limparExpirados(agora)
		}
		l.contador[chave] = &contadorJanela{inicio: agora, total: 1}
		return true
	}
	if c.total >= l.max {
		return false
	}
	c.total++
	return true
}

func (l *LimiteRequisicoes) limparExpirados(agora time.Time) {
	for chave, c := range l.contador {
		if agora.Sub(c.inicio) >= l.janela {
			delete(l.contador, chave)
		}
	}
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
