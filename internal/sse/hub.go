// package sse distribui eventos para as aplicacoes via Server-Sent Events.
//
// A chave de assinatura e "aplicacao:destino" (barramento, fase 3), e o
// destino e uma string OPACA cujo dono e a aplicacao -- o gateway nao
// resolve destino para pessoa, nao faz join com `usuario` e nao sabe se
// aquilo e gente, setor, robo ou fila (secao 1 do plano do barramento).
// Quem decide que alguem pode ler um destino e a aplicacao, no momento em
// que pede o token de sessao; o hub so honra o que o token diz.
//
// Antes da fase 3 a chave era o corretorID e existia um PublicarTodos que
// entregava toda mensagem interna a toda sessao conectada. Isso so era
// tolerável com um unico consumidor de cinco pessoas -- com N aplicacoes
// e vazamento entre empresas do mesmo prédio. Ver PublicarNaAplicacao
// abaixo para o que sobrou dele, e por quê.
package sse

import (
	"strings"
	"sync"
)

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
	mu sync.Mutex
	// chave = "aplicacao:destino" (ver ChaveDestino).
	assinantes map[string]map[chan Evento]struct{}
}

func NovoHub() *Hub {
	return &Hub{assinantes: make(map[string]map[chan Evento]struct{})}
}

// Assinar registra um canal de eventos para a chave "aplicacao:destino".
// cancelar deve ser chamado quando a conexao SSE terminar (defer no
// handler), para nao vazar o canal nem a entrada no mapa.
func (h *Hub) Assinar(chave string) (ch <-chan Evento, cancelar func()) {
	canal := make(chan Evento, tamanhoBufferAssinante)

	h.mu.Lock()
	if h.assinantes[chave] == nil {
		h.assinantes[chave] = make(map[chan Evento]struct{})
	}
	h.assinantes[chave][canal] = struct{}{}
	h.mu.Unlock()

	cancelar = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, existe := h.assinantes[chave][canal]; !existe {
			return
		}
		delete(h.assinantes[chave], canal)
		if len(h.assinantes[chave]) == 0 {
			delete(h.assinantes, chave)
		}
		close(canal)
	}
	return canal, cancelar
}

// Publicar entrega o evento a quem estiver assinando cada uma das chaves.
//
// Lista vazia nao entrega nada, e isso e o comportamento correto: se um
// evento nao tem destinatario listado, ele nao sai. O broadcast que
// existia antes era o contrario disso -- entregava a todos quando nao
// sabia a quem entregar.
func (h *Hub) Publicar(chaves []string, evento Evento) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, chave := range chaves {
		for canal := range h.assinantes[chave] {
			select {
			case canal <- evento:
			default:
				// assinante lento -- descarta em vez de travar o publicador.
				// o EventSource reconecta e a tela busca o estado atual de novo.
			}
		}
	}
}

// PublicarNaAplicacao entrega a todos os destinos de UMA aplicacao.
//
// E o que sobrou do PublicarTodos, e existe por duas entregas que ainda
// nao tem lista de destinos:
//
//  1. EventoFilaAtualizada -- conversa sem corretor atribuido nao tem a
//     quem endereçar, por definicao: o evento avisa que a fila mudou, e a
//     propria lista ja e filtrada por permissao do lado da aplicacao.
//  2. chat interno, enquanto o Poller for a fonte -- a lista de quem le
//     cada canal so passa a existir em `canal_assinante`, na fase 4.
//
// A diferenca para o PublicarTodos antigo e a fronteira: um evento do crm
// nao alcanca nenhum assinante do portal. O vazamento que a fase 3 fecha
// e o vazamento ENTRE APLICACOES, e esse esta fechado. O que resta e um
// broadcast dentro de uma aplicacao que ja recebia tudo.
//
// Some quando as duas entregas acima ganharem lista de destinos (fases 4
// e 5). Enquanto existir, e o unico ponto do hub que entrega sem destino
// explicito -- de propósito concentrado aqui, para ser um `git grep` e
// nao uma caça.
func (h *Hub) PublicarNaAplicacao(app string, evento Evento) {
	prefixo := app + ":"

	h.mu.Lock()
	defer h.mu.Unlock()
	for chave, canais := range h.assinantes {
		if !strings.HasPrefix(chave, prefixo) {
			continue
		}
		for canal := range canais {
			select {
			case canal <- evento:
			default:
			}
		}
	}
}
