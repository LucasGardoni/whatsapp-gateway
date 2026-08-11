package zapi

import (
	"fmt"
	"strings"
)

// TipoErro classifica o resultado de um envio para decidir se o outbox
// deve tentar novamente (fase 3).
type TipoErro int

const (
	ErroRetentavel TipoErro = iota
	ErroNaoRetentavel
	ErroShadowban
)

// padroesShadowban sao as strings documentadas na secao 4.8 do plano.
// Reconectar ou recriar instancia nao resolve shadowban -- so aguardar.
var padroesShadowban = []string{
	"shadow ban",
	"rejected sending this message",
}

func ehShadowban(mensagem string) bool {
	m := strings.ToLower(mensagem)
	for _, p := range padroesShadowban {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// ClassificarErro decide o tipo de erro a partir do status HTTP e da
// mensagem reportada pela Z-API (resposta de send-text ou webhook
// on-message-send). statusHTTP == 0 indica falha de transporte.
func ClassificarErro(statusHTTP int, mensagem string) TipoErro {
	if ehShadowban(mensagem) {
		return ErroShadowban
	}
	if statusHTTP == 0 || statusHTTP >= 500 {
		return ErroRetentavel
	}
	return ErroNaoRetentavel
}

// ErroEnvio embrulha a classificacao para quem chama Enviar decidir se
// reenfileira a mensagem.
type ErroEnvio struct {
	Tipo       TipoErro
	StatusHTTP int
	Mensagem   string
}

func (e *ErroEnvio) Error() string {
	return fmt.Sprintf("zapi: %s", e.Mensagem)
}

func (e *ErroEnvio) Retentavel() bool {
	return e.Tipo == ErroRetentavel
}
