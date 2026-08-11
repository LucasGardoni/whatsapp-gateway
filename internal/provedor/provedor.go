// package provedor abstrai o canal de atendimento do numero B -- trocavel
// porque a instancia Z-API pode ser banida (ver secao 3 do plano)
package provedor

import "context"

// Provedor e implementado por qualquer canal capaz de enviar texto ao
// numero B e reportar se a instancia esta conectada.
type Provedor interface {
	Enviar(ctx context.Context, msg MensagemTexto) (*ResultadoEnvio, error)
	Status(ctx context.Context) (*StatusInstancia, error)
}

// MensagemTexto e o texto a enviar. Destinatario aceita telefone E.164 ou
// @lid -- o provedor nao deve assumir qual dos dois recebeu.
type MensagemTexto struct {
	Destinatario string
	Texto        string
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
