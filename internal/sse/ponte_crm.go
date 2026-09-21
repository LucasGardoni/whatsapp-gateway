package sse

import "strconv"

// AplicacaoCRM e o codigo da aplicacao do CRM. Enquanto o CRM for o unico
// consumidor do caminho legado (/api/*), todo evento de WhatsApp pertence
// a ela -- quem recebe e um corretor do CRM, nao um destino opaco de uma
// aplicacao qualquer.
//
// Some na fase 8, junto com o caminho legado.
const AplicacaoCRM = "crm"

// ChaveCorretor traduz o corretorID do CRM para a chave opaca do hub
// (barramento, fase 3).
//
// Esta funcao e a ponte de compatibilidade inteira: e o unico lugar do
// gateway que ainda sabe que existe algo chamado "corretor". Depois da
// fase 8 ela sai, e o CRM passa a pedir sessao com destino opaco como
// qualquer outra aplicacao.
func ChaveCorretor(corretorID int64) string {
	return ChaveDestino(AplicacaoCRM, strconv.FormatInt(corretorID, 10))
}
