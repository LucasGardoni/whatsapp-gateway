package observabilidade

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type chaveRotulo struct{}

// rotulo e preenchido de dentro para fora: o middleware global cria, o
// autenticador de aplicacao (que roda depois) diz de quem e a requisicao.
type rotulo struct{ app string }

// MarcarAplicacao atribui a requisicao em andamento a uma aplicacao. Sem o
// middleware do coletor no caminho, nao faz nada.
func MarcarAplicacao(ctx context.Context, app string) {
	if r, ok := ctx.Value(chaveRotulo{}).(*rotulo); ok {
		r.app = app
	}
}

// caminhoStream fica fora da latencia: um EventSource dura horas e
// arrastaria o p99 de tudo. Ele e medido em SSEConectou.
const caminhoStream = "/eventos"

// Middleware mede toda requisicao, inclusive webhook e 401 -- e a carga
// que o servidor sentiu, autenticada ou nao.
func (c *Coletor) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == caminhoStream {
			next.ServeHTTP(w, r)
			return
		}

		rot := &rotulo{}
		r = r.WithContext(context.WithValue(r.Context(), chaveRotulo{}, rot))

		c.emAndamento.Add(1)
		defer c.emAndamento.Add(-1)

		inicio := time.Now()
		espiao := &espiao{ResponseWriter: w}
		next.ServeHTTP(espiao, r)

		// o padrao so existe depois do roteamento, por isso e lido aqui.
		rota := rotaNaoRoteada
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if padrao := rctx.RoutePattern(); padrao != "" {
				rota = r.Method + " " + padrao
			}
		}
		c.RequisicaoHTTP(rota, rot.app, time.Since(inicio), espiao.status())
	})
}

type espiao struct {
	http.ResponseWriter
	codigo int
}

func (e *espiao) WriteHeader(codigo int) {
	if e.codigo == 0 {
		e.codigo = codigo
	}
	e.ResponseWriter.WriteHeader(codigo)
}

func (e *espiao) Write(b []byte) (int, error) {
	if e.codigo == 0 {
		e.codigo = http.StatusOK
	}
	return e.ResponseWriter.Write(b)
}

// Flush e Unwrap mantem o que estiver por baixo acessivel: download de
// midia escreve em partes, e http.ResponseController procura o original.
func (e *espiao) Flush() {
	if f, ok := e.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (e *espiao) Unwrap() http.ResponseWriter { return e.ResponseWriter }

func (e *espiao) status() int {
	if e.codigo == 0 {
		return http.StatusOK
	}
	return e.codigo
}
