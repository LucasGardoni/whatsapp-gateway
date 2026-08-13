// package provedor abstrai o canal de atendimento do numero B -- trocavel
// porque a instancia Z-API pode ser banida (ver secao 3 do plano)
package provedor

import "context"

// Provedor e implementado por qualquer canal capaz de enviar texto ou
// midia ao numero B e reportar se a instancia esta conectada.
type Provedor interface {
	Enviar(ctx context.Context, msg MensagemTexto) (*ResultadoEnvio, error)
	EnviarMidia(ctx context.Context, msg MensagemMidia) (*ResultadoEnvio, error)
	Status(ctx context.Context) (*StatusInstancia, error)
}

// MensagemTexto e o texto a enviar. Destinatario aceita telefone E.164 ou
// @lid -- o provedor nao deve assumir qual dos dois recebeu.
type MensagemTexto struct {
	Destinatario string
	Texto        string
}

// MensagemMidia e o anexo a enviar (fase 9). Tipo segue o vocabulario de
// mensagem.tipo (imagem | audio | video | documento) e decide qual
// endpoint da z-api e chamado. ConteudoBase64 e o arquivo codificado como
// data URI (ver internal/midia.CodificarBase64) -- o provedor nao baixa
// nem le arquivo do disco, so fala HTTP.
type MensagemMidia struct {
	Destinatario   string
	Tipo           string
	ConteudoBase64 string
	NomeArquivo    string
	Legenda        string
}

// ResultadoEnvio traz os identificadores devolvidos pelo provedor.
// MessageID e o que deve ser gravado em mensagem.provedor_msg_id.
type ResultadoEnvio struct {
	ZaapID    string
	MessageID string
	ID        string
}

// StatusInstancia reflete /status -- alimenta o monitor de saude (fase 9).
type StatusInstancia struct {
	Conectada           bool
	SmartphoneConectado bool
	Detalhe             string
}

// ErroClassificado e implementado por erros de provedor que sabem dizer se
// merecem nova tentativa (ex.: zapi.ErroEnvio). Quem consome Provedor (o
// outbox) usa errors.As contra essa interface -- nunca importa zapi
// diretamente, senao perde a troca de provedor que justifica esta interface.
type ErroClassificado interface {
	error
	Retentavel() bool
}
