package dlp

import (
	"regexp"
	"strings"
)

var (
	regexEmail = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	regexCPF   = regexp.MustCompile(`\b\d{3}\.?\d{3}\.?\d{3}-?\d{2}\b`)
	regexUUID  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	regexVCard = regexp.MustCompile(`(?i)begin:vcard`)

	// tlds cobre os dominios mais comuns na operacao -- suficiente para
	// distinguir um link real de dois numeros separados por ponto.
	regexURL = regexp.MustCompile(`(?i)\b(?:https?://)?(?:www\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9-]+)*\.(?:com\.br|net\.br|org\.br|gov\.br|edu\.br|com|net|org|io|app|me|co|br)\b(?:/\S*)?`)

	// regexClusterNumerico casa um trecho continuo de digitos e separadores
	// tipicos de telefone -- letras no meio quebram o cluster (ver
	// detectarTelefone para o porque isso funciona contra falso-merge).
	regexClusterNumerico = regexp.MustCompile(`[\d\s\-.()/:]+`)

	regexNumeroPorExtenso = regexp.MustCompile(`\b(zero|um|uma|dois|duas|tres|quatro|cinco|seis|sete|oito|nove)\b`)
)

var digitoPorExtenso = map[string]string{
	"zero": "0", "um": "1", "uma": "1", "dois": "2", "duas": "2",
	"tres": "3", "quatro": "4", "cinco": "5", "seis": "6",
	"sete": "7", "oito": "8", "nove": "9",
}

func detectarEmail(texto string) (float64, bool) {
	if regexEmail.MatchString(texto) {
		return 0.95, true
	}
	return 0, false
}

func detectarCPF(texto string) (float64, bool) {
	if regexCPF.MatchString(texto) {
		return 0.7, true
	}
	return 0, false
}

func detectarVCard(texto string) (float64, bool) {
	if regexVCard.MatchString(texto) {
		return 0.99, true
	}
	return 0, false
}

func detectarFraseGatilho(texto string, frases []string) (float64, bool) {
	normalizado := normalizarTexto(texto)
	for _, frase := range frases {
		if strings.Contains(normalizado, normalizarTexto(frase)) {
			return 0.5, true
		}
	}
	return 0, false
}

func detectarLinkExterno(texto string, dominiosPermitidos []string) (float64, bool) {
	for _, url := range regexURL.FindAllString(texto, -1) {
		if !dominioPermitido(extrairDominio(url), dominiosPermitidos) {
			return 0.9, true
		}
	}
	return 0, false
}

// detectarPix nao tem formato proprio -- uma chave pix e um email, telefone,
// cpf ou uma chave aleatoria (uuid). O sinal e a palavra "pix" perto de um
// desses candidatos na mesma mensagem.
func detectarPix(texto string) (float64, bool) {
	if !strings.Contains(normalizarTexto(texto), "pix") {
		return 0, false
	}
	if regexEmail.MatchString(texto) || regexUUID.MatchString(texto) || regexCPF.MatchString(texto) {
		return 0.8, true
	}
	if _, ok := detectarTelefone(texto); ok {
		return 0.8, true
	}
	return 0, false
}

// detectarTelefone cobre a ofuscacao da secao 6: numero por extenso
// ("tres dois nove nove"), digitos espacados ("9 9 8 8") e prefixo textual
// ("zap: 32 nove nove"). A estrategia e converter numero por extenso em
// digito e depois procurar clusters continuos de digito+separador -- uma
// letra no meio quebra o cluster, o que naturalmente evita juntar "lote 32"
// com "bloco 9" em um numero so.
func detectarTelefone(textoOriginal string) (float64, bool) {
	texto := substituirNumerosPorExtenso(normalizarTexto(textoOriginal))

	melhor := 0.0
	achou := false
	for _, cluster := range regexClusterNumerico.FindAllString(texto, -1) {
		digitos := apenasDigitos(cluster)
		switch {
		case len(digitos) >= 10 && len(digitos) <= 13:
			achou = true
			melhor = max(melhor, 0.85)
		case len(digitos) >= 8 && len(digitos) <= 9:
			achou = true
			melhor = max(melhor, 0.6)
		}
	}
	return melhor, achou
}

func substituirNumerosPorExtenso(texto string) string {
	return regexNumeroPorExtenso.ReplaceAllStringFunc(texto, func(palavra string) string {
		return digitoPorExtenso[palavra]
	})
}

func apenasDigitos(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizarTexto baixa a caixa e remove acento -- so o suficiente para
// casar frase-gatilho e a palavra "pix" independente de como foi digitado.
func normalizarTexto(s string) string {
	substituicoes := map[rune]rune{
		'á': 'a', 'à': 'a', 'ã': 'a', 'â': 'a', 'ä': 'a',
		'é': 'e', 'ê': 'e', 'è': 'e',
		'í': 'i', 'ì': 'i',
		'ó': 'o', 'ô': 'o', 'õ': 'o', 'ò': 'o',
		'ú': 'u', 'ù': 'u', 'ü': 'u',
		'ç': 'c',
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if rep, ok := substituicoes[r]; ok {
			r = rep
		}
		b.WriteRune(r)
	}
	return b.String()
}

func extrairDominio(url string) string {
	d := strings.ToLower(url)
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "www.")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if i := strings.Index(d, ":"); i >= 0 {
		d = d[:i]
	}
	return d
}

func dominioPermitido(dominio string, permitidos []string) bool {
	for _, p := range permitidos {
		p = extrairDominio(p)
		if dominio == p || strings.HasSuffix(dominio, "."+p) {
			return true
		}
	}
	return false
}
