// package ingestao cobre a recepcao de leads de fontes externas (fase 11):
// webhook genérico por origem, normalizacao, dedup e importacao csv. A
// escrita de conversa/mensagem continua sendo do internal/matcher (fase 4)
// -- este pacote so cuida do momento em que o lead ainda nao existe.
package ingestao

// Entrada e o que um normalizador extrai de um payload bruto, antes de
// decidir se vira lead novo ou duplicata de um existente.
type Entrada struct {
	Nome             string
	TelefoneE164     string
	Origem           string
	EmpreendimentoID *int64
	AdSourceID       string
	CtwaClid         string
}

// Normalizador traduz o payload bruto de uma origem especifica pro
// vocabulario comum de Entrada. Nunca deve fazer IO -- so parse. Se a
// origem exigir uma chamada externa (ex.: Graph API pra resolver
// leadgen_id em field_data), isso e responsabilidade de quem monta o
// payload antes de mandar pro gateway, nao deste pacote (ver nota em
// meta_lead_ads.go).
type Normalizador func(payload []byte) (Entrada, error)

// Registro mapeia origem (path param do webhook) para o normalizador
// correspondente. Origem sem normalizador registrado nao e erro -- o
// bruto ja foi persistido antes de chegar aqui (secao 10, diretriz 7),
// so fica sem virar lead ate alguem implementar o parser dela.
type Registro map[string]Normalizador

// RegistroPadrao registra as origens conhecidas na v1 (fase 11).
func RegistroPadrao() Registro {
	return Registro{
		"meta-lead-ads": NormalizarMetaLeadAds,
		"generico":      NormalizarGenerico,
	}
}
