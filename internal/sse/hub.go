// package sse distribui eventos de mensagem para o CRM via Server-Sent
// Events (fase 7). Um so processo, um so hub -- nao precisa de pub/sub
// distribuido (secao 1 do plano: instancia unica).
//
// Mensagem interna (fase 10) nao passa por aqui do lado da escrita -- e
// gravada direto pelo CRM em mensagem_interna, sem tocar o gateway (secao
// 6 do plano: chat interno so compartilha o SSE, nao a tabela). O lado da
// leitura e o internal/chatinterno.Poller: ele faz polling da tabela e usa
// PublicarTodos abaixo pra entregar no mesmo hub -- quem esta em qual
// canal/DM e permissao de supervisor (secao 6, pendencia em aberto) e
// decisao do CRM, o gateway so retransmite pra todo mundo conectado.
package sse

import "sync"

// Evento e o payload entregue ao browser via EventSource. Tipo distingue
// mensagem nova (entrada ou saida) de mudanca de status de uma mensagem
// ja existente, ou mensagem interna nova (fase 10). CanalID so e
// preenchido em EventoMensagemInternaNova.
type Evento struct {
	Tipo       string `json:"tipo"` // mensagem_nova | mensagem_status | mensagem_interna_nova
	MensagemID int64  `json:"mensagem_id"`
	ConversaID int64  `json:"conversa_id,omitempty"`
	Status     string `json:"status,omitempty"`
	CanalID    int64  `json:"canal_id,omitempty"`
}

const (
	EventoMensagemNova        = "mensagem_nova"
	EventoMensagemStatus      = "mensagem_status"
	EventoMensagemInternaNova = "mensagem_interna_nova"
	// EventoFilaAtualizada avisa que algo mudou numa conversa SEM corretor
	// atribuido, ou seja, na fila de espera (fase 5). Tipo proprio, e nao
	// mensagem_nova para todos, porque senao a tela de conversa de cada
	// corretor reagiria a mensagem de um lead que nao e dele.
	EventoFilaAtualizada = "fila_atualizada"
)

// tamanhoBufferAssinante evita que o publicador bloqueie por causa de um
// assinante lento -- ver Publicar.
const tamanhoBufferAssinante = 16

type Hub struct {
	mu         sync.Mutex
	assinantes map[int64]map[chan Evento]struct{}
}

func NovoHub() *Hub {
	return &Hub{assinantes: make(map[int64]map[chan Evento]struct{})}
}

// Assinar registra um canal de eventos para o corretor. cancelar deve ser
// chamado quando a conexao SSE terminar (defer no handler), para nao
// vazar o canal nem a entrada no mapa.
func (h *Hub) Assinar(corretorID int64) (ch <-chan Evento, cancelar func()) {
	canal := make(chan Evento, tamanhoBufferAssinante)

	h.mu.Lock()
	if h.assinantes[corretorID] == nil {
		h.assinantes[corretorID] = make(map[chan Evento]struct{})
	}
	h.assinantes[corretorID][canal] = struct{}{}
	h.mu.Unlock()

	cancelar = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, existe := h.assinantes[corretorID][canal]; !existe {
			return
		}
		delete(h.assinantes[corretorID], canal)
		if len(h.assinantes[corretorID]) == 0 {
			delete(h.assinantes, corretorID)
		}
		close(canal)
	}
	return canal, cancelar
}

// Publicar entrega o evento a quem estiver assinando este corretor.
//
// corretorID nulo significa conversa ainda na fila de espera, sem dono.
// Antes isso nao publicava nada e o evento era simplesmente perdido: um
// lead novo chegava e a tela de Fila so mostrava depois que o corretor
// desse F5 -- justamente a tela onde a demora custa atendimento.
//
// Agora vira um EventoFilaAtualizada para todo mundo conectado. O tipo e
// outro de proposito: retransmitir mensagem_nova para todos faria a tela
// de conversa de cada corretor reagir a uma mensagem que nao e dele. Os
// campos originais sao descartados junto -- quem esta na Fila so precisa
// saber que a lista mudou, e a lista ja e filtrada por permissao no CRM.
func (h *Hub) Publicar(corretorID *int64, evento Evento) {
	if corretorID == nil {
		h.PublicarTodos(Evento{Tipo: EventoFilaAtualizada})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for canal := range h.assinantes[*corretorID] {
		select {
		case canal <- evento:
		default:
			// assinante lento -- descarta em vez de travar o publicador.
			// o EventSource reconecta e a tela busca o estado atual de novo.
		}
	}
}

// PublicarTodos entrega o evento a todo mundo conectado, independente do
// corretor -- usado pelo chat interno (fase 10), onde saber quem pertence
// a qual canal/DM e permissao de supervisor e decisao do CRM (secao 6),
// nao do gateway. O browser filtra o que e relevante pra tela aberta.
func (h *Hub) PublicarTodos(evento Evento) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, canais := range h.assinantes {
		for canal := range canais {
			select {
			case canal <- evento:
			default:
			}
		}
	}
}
