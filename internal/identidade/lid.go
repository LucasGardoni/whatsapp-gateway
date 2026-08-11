package identidade

import "strings"

// EhLid reconhece o @lid do WhatsApp. O campo phone do webhook pode conter
// isso em vez de um telefone -- nunca assuma que phone e sempre telefone.
func EhLid(valor string) bool {
	return strings.HasSuffix(valor, "@lid")
}
