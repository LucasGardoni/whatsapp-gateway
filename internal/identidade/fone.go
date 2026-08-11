// package identidade resolve a identidade do contato: telefone E.164 ou @lid
package identidade

import (
	"fmt"
	"strings"
)

// NormalizarE164 normaliza um telefone brasileiro para E.164, inserindo o
// nono digito em celular quando ausente. Telefone fixo (2 a 5 apos o DDD)
// nao recebe o nono digito.
func NormalizarE164(numero string) (string, error) {
	digitos := apenasDigitos(numero)
	digitos = removerDDI(digitos)

	if len(digitos) < 10 || len(digitos) > 11 {
		return "", fmt.Errorf("normalizar e164 %q: numero invalido", numero)
	}

	ddd := digitos[:2]
	if ddd[0] < '1' || ddd[0] > '9' {
		return "", fmt.Errorf("normalizar e164 %q: ddd invalido", numero)
	}

	assinante := digitos[2:]
	switch len(assinante) {
	case 8:
		if assinante[0] >= '6' && assinante[0] <= '9' {
			assinante = "9" + assinante
		}
	case 9:
		if assinante[0] != '9' {
			return "", fmt.Errorf("normalizar e164 %q: numero invalido", numero)
		}
	default:
		return "", fmt.Errorf("normalizar e164 %q: numero invalido", numero)
	}

	return "+55" + ddd + assinante, nil
}

// Iguais compara duas identidades (telefone ou @lid) considerando a variacao
// do nono digito. @lid nunca converte para telefone, entao so bate com ele
// mesmo, caractere a caractere.
func Iguais(a, b string) bool {
	if EhLid(a) || EhLid(b) {
		return a == b
	}

	na, erroA := NormalizarE164(a)
	nb, erroB := NormalizarE164(b)
	if erroA != nil || erroB != nil {
		return false
	}
	return na == nb
}

func apenasDigitos(valor string) string {
	var b strings.Builder
	for _, r := range valor {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// removerDDI remove o 55 do Brasil quando presente. So se aplica quando o
// tamanho indica DDI (12 ou 13 digitos); do contrario um DDD que comece com
// 55 seria removido por engano.
func removerDDI(digitos string) string {
	if (len(digitos) == 12 || len(digitos) == 13) && strings.HasPrefix(digitos, "55") {
		return digitos[2:]
	}
	return digitos
}
