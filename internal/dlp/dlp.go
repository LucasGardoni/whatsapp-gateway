// package dlp avalia o texto de uma mensagem de saida contra as regras da
// secao 6 do plano antes da entrega ao provedor -- e o unico ponto de
// controle, nenhum caminho de envio pode contornar (diretriz 10).
package dlp

// Modo e uma das tres decisoes possiveis para uma regra que bateu.
type Modo string

const (
	Bloquear Modo = "bloquear"
	Avisar   Modo = "avisar"
	Liberar  Modo = "liberar"
)

// nomes das regras -- tambem gravados na coluna dlp_ocorrencia.regra.
const (
	RegraTelefone     = "telefone"
	RegraEmail        = "email"
	RegraCPF          = "cpf"
	RegraPix          = "pix"
	RegraLinkExterno  = "link_externo"
	RegraVCard        = "vcard"
	RegraFraseGatilho = "frase_gatilho"
)

// Ocorrencia e uma regra que bateu no texto avaliado, com a decisao ja
// resolvida pelo motor (calibragem mais eventuais overrides de Config).
type Ocorrencia struct {
	Regra     string
	Decisao   Modo
	Confianca float64
}

// Resultado e o veredito completo de uma avaliacao.
type Resultado struct {
	Ocorrencias []Ocorrencia
}

// Bloqueado e true quando ao menos uma ocorrencia decidiu bloquear -- a
// mensagem nao deve ser entregue ao provedor.
func (r Resultado) Bloqueado() bool {
	for _, o := range r.Ocorrencias {
		if o.Decisao == Bloquear {
			return true
		}
	}
	return false
}
