package mensagem

import (
	"strings"
	"unicode"
)

// TipoContato e o cartao de contato (vCard) do WhatsApp.
const TipoContato = "contato"

// Contato e o cartao trocado na conversa. Em mensagem.texto ele fica como
// o nome na primeira linha e um telefone (so digitos) por linha depois:
// legivel na busca e no DLP sem coluna nova.
type Contato struct {
	Nome      string
	Telefones []string
}

// FormatarContato monta o texto gravado em mensagem.texto.
func FormatarContato(c Contato) string {
	linhas := []string{strings.TrimSpace(strings.ReplaceAll(c.Nome, "\n", " "))}
	for _, t := range c.Telefones {
		if d := SoDigitos(t); d != "" {
			linhas = append(linhas, d)
		}
	}
	return strings.Join(linhas, "\n")
}

// LerContato desfaz FormatarContato.
func LerContato(texto string) Contato {
	linhas := strings.Split(strings.ReplaceAll(texto, "\r\n", "\n"), "\n")
	c := Contato{Nome: strings.TrimSpace(linhas[0])}
	for _, l := range linhas[1:] {
		if d := SoDigitos(l); d != "" {
			c.Telefones = append(c.Telefones, d)
		}
	}
	return c
}

// SoDigitos tira tudo que nao e digito ("+55 (31) 9 9999-0000" -> "5531999990000").
func SoDigitos(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

// TelefonesDoVCard le as linhas TEL de um vCard -- o fallback quando a
// z-api nao manda a lista `phones` pronta. Prefere o waid (numero do
// WhatsApp) ao texto exibido.
func TelefonesDoVCard(vcard string) []string {
	var telefones []string
	for _, linha := range strings.Split(strings.ReplaceAll(vcard, "\r\n", "\n"), "\n") {
		chave, valor, ok := strings.Cut(linha, ":")
		if !ok {
			continue
		}
		chaveMaiuscula := strings.ToUpper(chave)
		if !strings.HasPrefix(chaveMaiuscula, "TEL") && !strings.Contains(chaveMaiuscula, ".TEL") {
			continue
		}
		numero := valor
		if _, waid, ok := strings.Cut(chaveMaiuscula, "WAID="); ok {
			if d := SoDigitos(strings.SplitN(waid, ";", 2)[0]); d != "" {
				numero = d
			}
		}
		if d := SoDigitos(numero); d != "" {
			telefones = append(telefones, d)
		}
	}
	return telefones
}
