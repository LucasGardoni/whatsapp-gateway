package dlp

// Config parametriza o motor. Modos tem default pela calibragem inicial da
// secao 6 -- so precisa preencher o que for diferente do padrao.
//
// SomenteAvisar rebaixa toda decisao "bloquear" para "avisar" sem tocar em
// avisar/liberar. E o interruptor operacional para a entrada em producao
// descrita na secao 6: "tudo em avisar por 2 semanas" antes de calibrar com
// falsos positivos reais.
type Config struct {
	Modos map[string]Modo

	FrasesGatilho      []string
	DominiosPermitidos []string

	SomenteAvisar bool
}

// modosPadrao e a calibragem inicial da secao 6 do plano.
var modosPadrao = map[string]Modo{
	RegraTelefone:     Bloquear,
	RegraEmail:        Bloquear,
	RegraCPF:          Bloquear,
	RegraPix:          Bloquear,
	RegraLinkExterno:  Bloquear,
	RegraVCard:        Bloquear,
	RegraFraseGatilho: Avisar,
}

var frasesGatilhoPadrao = []string{
	"me chama",
	"meu particular",
	"fora daqui",
}

func (c Config) comDefaults() Config {
	modos := make(map[string]Modo, len(modosPadrao))
	for regra, modo := range modosPadrao {
		modos[regra] = modo
	}
	for regra, modo := range c.Modos {
		modos[regra] = modo
	}
	c.Modos = modos

	if len(c.FrasesGatilho) == 0 {
		c.FrasesGatilho = frasesGatilhoPadrao
	}
	return c
}

type Motor struct {
	cfg Config
}

func NovoMotor(cfg Config) *Motor {
	return &Motor{cfg: cfg.comDefaults()}
}

// Avaliar roda todos os detectores sobre o texto de saida e devolve uma
// ocorrencia por regra que bateu, com o modo ja resolvido.
func (m *Motor) Avaliar(texto string) Resultado {
	achados := map[string]float64{}

	if confianca, ok := detectarTelefone(texto); ok {
		achados[RegraTelefone] = confianca
	}
	if confianca, ok := detectarEmail(texto); ok {
		achados[RegraEmail] = confianca
	}
	if confianca, ok := detectarCPF(texto); ok {
		achados[RegraCPF] = confianca
	}
	if confianca, ok := detectarPix(texto); ok {
		achados[RegraPix] = confianca
	}
	if confianca, ok := detectarLinkExterno(texto, m.cfg.DominiosPermitidos); ok {
		achados[RegraLinkExterno] = confianca
	}
	if confianca, ok := detectarVCard(texto); ok {
		achados[RegraVCard] = confianca
	}
	if confianca, ok := detectarFraseGatilho(texto, m.cfg.FrasesGatilho); ok {
		achados[RegraFraseGatilho] = confianca
	}

	var resultado Resultado
	for regra, confianca := range achados {
		resultado.Ocorrencias = append(resultado.Ocorrencias, Ocorrencia{
			Regra:     regra,
			Decisao:   m.resolverModo(regra),
			Confianca: confianca,
		})
	}
	return resultado
}

func (m *Motor) resolverModo(regra string) Modo {
	modo := m.cfg.Modos[regra]
	if modo == "" {
		modo = Avisar // regra sem modo configurado nunca deve liberar em silencio
	}
	if m.cfg.SomenteAvisar && modo == Bloquear {
		return Avisar
	}
	return modo
}
