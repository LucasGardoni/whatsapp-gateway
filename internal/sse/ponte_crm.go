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

// PublicarParaCorretorCRM mantem o contrato antigo dos publicadores de
// WhatsApp (webhook, outbox, handler de mensagens), que conhecem um
// *int64 e nao uma chave.
//
// corretorID nulo significa conversa ainda na fila de espera, sem dono.
// Antes isso nao publicava nada e o evento era perdido: um lead novo
// chegava e a tela de Fila so mostrava depois de um F5 -- justamente a
// tela onde a demora custa atendimento. Vira um EventoFilaAtualizada para
// a aplicacao do CRM. O tipo e outro de proposito: retransmitir
// mensagem_nova faria a tela de conversa de cada corretor reagir a uma
// mensagem que nao e dele. Os campos originais sao descartados junto --
// quem esta na Fila so precisa saber que a lista mudou, e a lista ja e
// filtrada por permissao no CRM.
func (h *Hub) PublicarParaCorretorCRM(corretorID *int64, evento Evento) {
	if corretorID == nil {
		h.PublicarNaAplicacao(AplicacaoCRM, Evento{Tipo: EventoFilaAtualizada})
		return
	}
	h.Publicar([]string{ChaveCorretor(*corretorID)}, evento)
}
