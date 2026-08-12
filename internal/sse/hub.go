// package sse distribui eventos de mensagem para o CRM via Server-Sent
// Events (fase 7). Um so processo, um so hub -- nao precisa de pub/sub
// distribuido (secao 1 do plano: instancia unica).
//
// Mensagem interna (fase 10) nao passa por aqui -- e escrita direto pelo
// CRM em mensagem_interna, sem tocar o gateway (secao 6 do plano: chat
// interno so compartilha o SSE, nao a tabela). Cobrir esse caso e decisao
// em aberto, registrada no plano do CRM (secao 7): provavelmente um
// segundo mecanismo, por polling, e nao este hub em memoria.
package sse

import "sync"

// Evento e o payload entregue ao browser via EventSource. Tipo distingue
// mensagem nova (entrada ou saida) de mudanca de status de uma mensagem
// ja existente.
type Evento struct {
	Tipo       string `json:"tipo"` // mensagem_nova | mensagem_status
	MensagemID int64  `json:"mensagem_id"`
	ConversaID int64  `json:"conversa_id"`
	Status     string `json:"status"`
}

const (
	EventoMensagemNova   = "mensagem_nova"
	EventoMensagemStatus = "mensagem_status"
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
// corretorID nulo (conversa ainda na fila de espera, sem corretor
// atribuido) nao publica nada -- ninguem assina uma conversa sem dono.
func (h *Hub) Publicar(corretorID *int64, evento Evento) {
	if corretorID == nil {
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
