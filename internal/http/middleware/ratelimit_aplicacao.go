package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// LimitePorAplicacao limita requisicoes por APLICACAO nas rotas
// autenticadas (barramento, fase 9).
//
// O motivo de existir esta no plano: "uma plataforma nova em loop nao
// pode derrubar o canal das outras". O limite por IP nao resolve isso --
// atras de um proxy reverso todas as aplicacoes chegam com o mesmo IP (o
// do proxy, ou o da rede interna), entao ou o teto e alto o bastante para
// nao proteger ninguem, ou uma aplicacao em laco consome o teto de todas.
// A chave correta e a identidade que a fase 1 criou.
//
// Roda DEPOIS de AutenticadorAplicacao, e depende disso: sem aplicacao no
// contexto ele deixa passar, porque nao ha o que limitar e recusar aqui
// esconderia o 401 que o proximo handler ja daria.
type LimitePorAplicacao struct {
	janela janelaFixa
	// maxPadrao vale para quem nao tem limite proprio em
	// aplicacao.limite_requisicoes_por_minuto. <= 0 desliga o limite
	// (fail open), mesmo contrato de NovoLimiteRequisicoes.
	maxPadrao int
}

func NovoLimitePorAplicacao(maxPadrao int, janela time.Duration) *LimitePorAplicacao {
	return &LimitePorAplicacao{janela: novaJanelaFixa(janela), maxPadrao: maxPadrao}
}

// Middleware conta por codigo de aplicacao e usa o teto que a LINHA da
// aplicacao manda -- nunca um ramo por codigo (secao 2, item 6 do plano).
// Por isso o limite chega aqui dentro da struct Aplicacao, e nao de um
// mapa de excecoes neste arquivo.
func (l *LimitePorAplicacao) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app, autenticada := AplicacaoDoContexto(r.Context())
		if !autenticada {
			next.ServeHTTP(w, r)
			return
		}

		max := l.maxPadrao
		if app.LimiteRequisicoesPorMinuto != nil {
			max = int(*app.LimiteRequisicoesPorMinuto)
		}
		if max <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		if !l.janela.permitir(app.Codigo, max) {
			// Warn e nao Info: 429 por aplicacao e quase sempre bug de
			// integracao do outro lado, e quem opera o gateway e o unico
			// que consegue ver. Sem esta linha, o sintoma do outro lado e
			// "o gateway esta lento" e nao ha nada no log que explique.
			slog.Warn("rate limit por aplicacao estourado",
				"aplicacao", app.Codigo, "limite_por_minuto", max, "rota", r.URL.Path)
			responder429(w, l.janela.janela)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// responder429 sempre manda Retry-After. Sem ele, um cliente que leva 429
// nao sabe se espera 1s ou 10min, e o comportamento tipico e retentar em
// laco -- transformando a protecao na causa da carga que ela existe para
// conter.
//
// A janela inteira, e nao o que resta dela: o que resta depende de quando
// a janela desta chave comecou, e devolver isso diria a um estranho o
// ritmo de quem mais bate no mesmo endpoint.
func responder429(w http.ResponseWriter, janela time.Duration) {
	segundos := int(janela.Seconds())
	if segundos < 1 {
		segundos = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(segundos))
	http.Error(w, "muitas requisicoes, tente novamente em instantes", http.StatusTooManyRequests)
}
