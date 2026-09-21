package mensagem

import (
	"encoding/base64"
	"strings"
)

// Canais aceitos em POST /v1/mensagens.
//
// O campo `canal` e o unico discriminador da entrada unificada (fase 6):
// tudo antes dele -- autenticacao, decodificacao, transacao, commit e
// publicacao -- e comum aos dois, e tudo depois e do Entregador. A lista e
// fechada porque um canal desconhecido tem de ser 400 na entrada, e nao
// uma mensagem gravada em lugar nenhum.
const (
	CanalWhatsApp = "whatsapp"
	CanalInterno  = "interno"
)

// Requisicao e o corpo de POST /v1/mensagens -- a uniao dos dois canais.
//
// Uniao, e nao duas structs: o endpoint e um so (secao 3 do plano, "uma
// entrada"), e quem integra manda o mesmo JSON mudando `canal`. Os campos
// que nao pertencem ao canal escolhido sao simplesmente ignorados; recusar
// por campo estranho transformaria cada adicao futura em quebra de
// compatibilidade para quem manda o payload inteiro.
//
// Serve tambem a rota legada POST /api/mensagens, que decodifica nela com
// `canal` implicito -- e por isso que os dois caminhos entregam o mesmo
// resultado: e literalmente o mesmo codigo.
type Requisicao struct {
	Canal string `json:"canal"`

	// canal=whatsapp -- texto em claro obrigatorio, porque o DLP precisa
	// conseguir ler (secao 3).
	ConversaID   int64  `json:"conversa_id"`
	Tipo         string `json:"tipo"`
	Texto        string `json:"texto"`
	MidiaCaminho string `json:"midia_caminho"`

	// canal=interno -- opaco de ponta a ponta. ConteudoCifrado chega em
	// base64 (nonce || ciphertext) so porque JSON nao carrega bytes.
	CanalExterno    string `json:"canal_externo"`
	Remetente       string `json:"remetente"`
	ConteudoCifrado string `json:"conteudo_cifrado"`
	CifraAlg        string `json:"cifra_alg"`
	CifraVersao     int32  `json:"cifra_versao"`
}

// TipoTexto e o tipo default de mensagem de WhatsApp.
const TipoTexto = "texto"

// tiposMidiaAceitos sao os valores de mensagem.tipo que levam arquivo. A
// lista casa com o CHECK do schema (migration 00003) e com o switch de
// camposEnvioMidia no cliente da z-api -- se divergir, a mensagem e aceita
// aqui e morre no outbox como "tipo nao suportado", que e o pior lugar
// para descobrir.
var tiposMidiaAceitos = map[string]bool{
	"imagem":    true,
	"audio":     true,
	"video":     true,
	"documento": true,
}

// WhatsApp e uma mensagem de saida para o WhatsApp, ja normalizada.
//
// Ao contrario da Interna, aqui o texto vem em claro -- e obrigatorio que
// venha: o DLP so consegue bloquear o que consegue ler. Essa e a diferenca
// que a secao 3 do plano chama de bifurcacao, e ela comeca ja na entrada.
type WhatsApp struct {
	ConversaID   int64
	Tipo         string
	Texto        string
	MidiaCaminho string
}

// ComoWhatsApp normaliza a requisicao para o canal WhatsApp. Tipo vazio
// vira "texto", mantendo compativel quem ja chamava /api/mensagens so com
// conversa_id e texto.
func (r Requisicao) ComoWhatsApp() WhatsApp {
	m := WhatsApp{
		ConversaID:   r.ConversaID,
		Tipo:         strings.TrimSpace(r.Tipo),
		Texto:        strings.TrimSpace(r.Texto),
		MidiaCaminho: strings.TrimSpace(r.MidiaCaminho),
	}
	if m.Tipo == "" {
		m.Tipo = TipoTexto
	}
	return m
}

// Validar recusa o que nao da para enviar. Toda falha aqui e ErroValidacao.
//
// O confinamento de MidiaCaminho a MIDIA_DIR NAO esta aqui: depende da
// configuracao do processo, nao da mensagem. Fica no Entregador, que a
// conhece.
func (m WhatsApp) Validar() error {
	if m.ConversaID == 0 {
		return invalido("conversa_id e obrigatorio")
	}
	switch {
	case m.Tipo == TipoTexto:
		if m.Texto == "" {
			return invalido("texto e obrigatorio para tipo texto")
		}
		if m.MidiaCaminho != "" {
			return invalido("midia_caminho nao se aplica ao tipo texto")
		}
	case tiposMidiaAceitos[m.Tipo]:
		if m.MidiaCaminho == "" {
			return invalido("midia_caminho e obrigatorio para tipo %s", m.Tipo)
		}
	default:
		return invalido("tipo invalido: use texto, imagem, audio, video ou documento")
	}
	return nil
}

// ComoInterna normaliza a requisicao para o canal interno e desfaz o
// envelope base64.
//
// Decodificar nao e ler: o gateway desfaz o envelope que o JSON exigiu e
// guarda os bytes como vieram. Base64 quebrado e erro de transporte, e por
// isso vira ErroValidacao aqui e nao falha de gateway.
func (r Requisicao) ComoInterna() (Interna, error) {
	conteudo, err := base64.StdEncoding.DecodeString(strings.TrimSpace(r.ConteudoCifrado))
	if err != nil {
		return Interna{}, invalido("conteudo_cifrado deve ser base64")
	}
	return Interna{
		CanalExterno:    strings.TrimSpace(r.CanalExterno),
		Remetente:       strings.TrimSpace(r.Remetente),
		ConteudoCifrado: conteudo,
		CifraAlg:        strings.TrimSpace(r.CifraAlg),
		CifraVersao:     r.CifraVersao,
	}, nil
}
