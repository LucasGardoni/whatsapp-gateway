package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

// HeaderRequestID e o nome do header de correlacao. Mesmo nome nos dois
// lados da integracao -- o CRM manda ao chamar /api/*, o gateway repete na
// resposta e usa em todo log daquela requisicao.
const HeaderRequestID = "X-Request-Id"

type chaveRequestID struct{}

// RequestID propaga (ou cria) um id de correlacao por requisicao.
//
// Sem isso, investigar "a mensagem do cliente X nao chegou" significa
// cruzar a mao o log do CRM com o do gateway por horario aproximado -- e os
// dois relogios ja provaram divergir (P1-08). Com o id, uma requisicao e
// uma string procuravel nos dois lados.
//
// Respeita o id que o chamador mandou, porque quem comeca a cadeia e o CRM:
// gerar um novo aqui quebraria a correlacao justamente no ponto que ela
// existe para ligar.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !idRequisicaoValido(id) {
			id = gerarRequestID()
		}

		// devolve no header para o chamador poder registrar o mesmo id --
		// inclusive quando ele nao mandou nenhum e o gateway gerou.
		w.Header().Set(HeaderRequestID, id)

		ctx := context.WithValue(r.Context(), chaveRequestID{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// idRequisicaoValido recusa id vazio, longo demais ou com caractere fora do
// conjunto seguro. Ele vai para dentro de linhas de log: aceitar texto
// arbitrario de fora permitiria injetar quebra de linha e forjar entradas de
// log falsas (log injection).
func idRequisicaoValido(id string) bool {
	const maxTamanho = 64
	if id == "" || len(id) > maxTamanho {
		return false
	}
	for _, c := range id {
		alfanumerico := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alfanumerico && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func gerarRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// sem entropia o id perde utilidade, mas derrubar a requisicao por
		// causa de observabilidade seria pior que servi-la sem correlacao.
		return "sem-id"
	}
	return hex.EncodeToString(b[:])
}

// RequestIDDoContexto devolve o id de correlacao da requisicao, ou "" fora
// de uma requisicao HTTP (worker, job agendado).
func RequestIDDoContexto(ctx context.Context) string {
	id, _ := ctx.Value(chaveRequestID{}).(string)
	return id
}

// LoggerDoContexto devolve um slog.Logger ja com o request_id preso, para o
// handler nao precisar repetir o campo em cada chamada de log -- repetir a
// mao e o que faz alguns logs saírem sem correlacao.
func LoggerDoContexto(ctx context.Context) *slog.Logger {
	if id := RequestIDDoContexto(ctx); id != "" {
		return slog.With("request_id", id)
	}
	return slog.Default()
}
