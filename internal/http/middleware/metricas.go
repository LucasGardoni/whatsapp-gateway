package middleware

import (
	"net/http"

	"github.com/LucasGardoni/whatsapp-gateway/internal/metrica"
)

// Metricas conta requisicao e erro por aplicacao (barramento, fase 9).
//
// Fica no middleware, e nao em cada handler, por uma razao so: handler
// novo nasce medido. Instrumentar handler por handler garante que o
// proximo endpoint entre sem metrica e ninguem note, e o buraco aparece
// justamente quando se vai investigar algo.
//
// Roda DEPOIS de AutenticadorAplicacao -- requisicao sem aplicacao nao e
// contada, porque nao ha a quem atribuir. Isso deixa fora de proposito o
// 401 de token errado: quem o produz nao tem identidade, e contar isso
// numa chave inventada ("desconhecido") criaria uma aplicacao que nao
// existe no painel.
func Metricas(registro *metrica.Registro) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			app, autenticada := AplicacaoDoContexto(r.Context())
			if !autenticada {
				next.ServeHTTP(w, r)
				return
			}

			espiao := &espiaoStatus{ResponseWriter: w}
			next.ServeHTTP(espiao, r)
			registro.Requisicao(app.Codigo, espiao.status() >= http.StatusBadRequest)
		})
	}
}

// espiaoStatus guarda o codigo de status para o middleware ler depois.
//
// Nao implementa Flusher de proposito: /eventos (o unico stream) nao
// passa por este middleware -- e autenticado por token de sessao, nao por
// token de aplicacao, e conta a conexao pelo gauge de SSE. Se algum dia
// uma rota de stream entrar aqui, este tipo precisa de Flush, senao o
// stream para de escoar.
type espiaoStatus struct {
	http.ResponseWriter
	codigo int
}

func (e *espiaoStatus) WriteHeader(codigo int) {
	if e.codigo == 0 {
		e.codigo = codigo
	}
	e.ResponseWriter.WriteHeader(codigo)
}

// Write cobre o handler que escreve corpo sem chamar WriteHeader -- o
// net/http assume 200 nesse caso, e sem isto a requisicao apareceria com
// status 0 e seria contada como sucesso por acidente, nao por leitura.
func (e *espiaoStatus) Write(b []byte) (int, error) {
	if e.codigo == 0 {
		e.codigo = http.StatusOK
	}
	return e.ResponseWriter.Write(b)
}

// status devolve 200 quando o handler nao escreveu nada: e o que o
// net/http envia ao terminar sem escrever.
func (e *espiaoStatus) status() int {
	if e.codigo == 0 {
		return http.StatusOK
	}
	return e.codigo
}
