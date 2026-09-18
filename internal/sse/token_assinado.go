package sse

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ValidadeTokenAssinado e curta de proposito, e mais curta que os 5
// minutos do TokenStore antigo.
//
// O token antigo era de USO UNICO: validar consumia a linha em memoria.
// Um token assinado nao pode ser de uso unico sem estado compartilhado --
// que e exatamente o que se quer eliminar para rodar mais de uma
// instancia (secao 7.1 do plano do barramento). A compensacao e o prazo:
// o token serve para ABRIR uma conexao, nao para mante-la, e 60s e
// folgado para o browser conectar e curto demais para valer roubo.
//
// Esta e uma reducao de garantia assumida, nao um detalhe -- risco R2 da
// secao 9.
const ValidadeTokenAssinado = 60 * time.Second

// ErroTokenInvalido cobre token malformado, assinatura errada e token
// expirado -- de proposito indistinguiveis para quem chama. Dizer ao
// cliente QUAL dos tres falhou entrega um oraculo para quem esta
// tentando forjar.
var ErroTokenInvalido = errors.New("sse: token invalido ou expirado")

// SessaoToken e o que o token carrega. Nada aqui identifica pessoa: Dst e
// um destino opaco cujo dono e a aplicacao (secao 1 do plano). O gateway
// nao sabe, e nao pode descobrir, se aquilo e gente, setor, robo ou fila.
type SessaoToken struct {
	// App e o codigo da aplicacao que emitiu -- e o que impede um destino
	// "u-1" do portal receber o evento do "u-1" do crm.
	App string `json:"app"`
	Dst string `json:"dst"`
	Exp int64  `json:"exp"`
	// Jti nao e verificado contra nada (nao ha estado), mas garante que
	// dois tokens emitidos no mesmo segundo para o mesmo destino sejam
	// bytes diferentes. Sem ele, o token viraria uma funcao deterministica
	// de (app, dst, segundo) -- e um token vazado valeria para toda a
	// janela, nao so para aquela emissao.
	Jti string `json:"jti"`
}

// Chave e a chave do hub para este token: "app:destino". A composicao e
// aqui, num lugar so, porque ela E a fronteira entre aplicacoes -- se dois
// pontos do codigo montassem a chave de formas diferentes, a fronteira
// vazaria no que divergisse.
func (s SessaoToken) Chave() string { return ChaveDestino(s.App, s.Dst) }

// ChaveDestino compoe a chave de assinatura do hub.
func ChaveDestino(app, destino string) string { return app + ":" + destino }

// AssinadorSessao emite e valida token de sessao SSE sem guardar estado:
// qualquer instancia do gateway valida o que qualquer outra emitiu, desde
// que compartilhem a chave (SSE_SIGNING_KEY). E o que derruba o teto de
// instancia unica do TokenStore antigo.
type AssinadorSessao struct {
	chave []byte
	agora func() time.Time
}

// NovoAssinadorSessao devolve nil quando a chave e vazia. Quem chama
// precisa tratar esse nil fechando o endpoint (fail closed, padrao da
// base) -- um assinador que "funciona" sem chave assinaria com segredo
// vazio, e qualquer um forjaria token para qualquer destino.
func NovoAssinadorSessao(chave string) *AssinadorSessao {
	if chave == "" {
		return nil
	}
	return &AssinadorSessao{chave: []byte(chave), agora: time.Now}
}

// Emitir assina um token para o par (aplicacao, destino).
//
// Nao ha verificacao nenhuma de que o destino existe ou de que alguem tem
// direito a ele: isso e decisao da aplicacao, tomada ANTES de chamar aqui
// (secao 1 do plano). A permissao e consequencia de quem emitiu o token.
func (a *AssinadorSessao) Emitir(app, destino string) (token string, expiraEm time.Time, err error) {
	if app == "" || destino == "" {
		return "", time.Time{}, errors.New("sse: aplicacao e destino sao obrigatorios")
	}

	jti := make([]byte, 12)
	if _, err = rand.Read(jti); err != nil {
		return "", time.Time{}, fmt.Errorf("sse: gerar jti: %w", err)
	}

	expiraEm = a.agora().Add(ValidadeTokenAssinado)
	corpo, err := json.Marshal(SessaoToken{
		App: app,
		Dst: destino,
		Exp: expiraEm.Unix(),
		Jti: base64.RawURLEncoding.EncodeToString(jti),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sse: serializar token: %w", err)
	}

	corpoB64 := base64.RawURLEncoding.EncodeToString(corpo)
	return corpoB64 + "." + a.assinar(corpoB64), expiraEm, nil
}

// Validar confere a assinatura e o prazo.
//
// A assinatura e conferida ANTES do JSON ser lido: sem isso, o parser
// processaria bytes que ninguem assinou, o que e superficie de ataque de
// graca.
func (a *AssinadorSessao) Validar(token string) (SessaoToken, error) {
	corpoB64, assinatura, achou := strings.Cut(token, ".")
	if !achou || corpoB64 == "" || assinatura == "" {
		return SessaoToken{}, ErroTokenInvalido
	}

	// tempo constante: comparacao byte a byte com saida antecipada vaza,
	// por tempo de resposta, quantos bytes do prefixo o atacante acertou --
	// o que transforma forjar a assinatura num problema linear em vez de
	// exponencial.
	if !hmac.Equal([]byte(assinatura), []byte(a.assinar(corpoB64))) {
		return SessaoToken{}, ErroTokenInvalido
	}

	corpo, err := base64.RawURLEncoding.DecodeString(corpoB64)
	if err != nil {
		return SessaoToken{}, ErroTokenInvalido
	}

	var sessao SessaoToken
	if err := json.Unmarshal(corpo, &sessao); err != nil {
		return SessaoToken{}, ErroTokenInvalido
	}
	if sessao.App == "" || sessao.Dst == "" {
		return SessaoToken{}, ErroTokenInvalido
	}
	if a.agora().After(time.Unix(sessao.Exp, 0)) {
		return SessaoToken{}, ErroTokenInvalido
	}

	return sessao, nil
}

func (a *AssinadorSessao) assinar(corpoB64 string) string {
	mac := hmac.New(sha256.New, a.chave)
	mac.Write([]byte(corpoB64))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
