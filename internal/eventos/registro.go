// package eventos e o barramento entre INSTANCIAS do gateway (fase 7 do
// plano do barramento).
//
// Ate a fase 6, quem gravava uma mensagem publicava direto no hub em
// memoria do proprio processo. Funciona com uma instancia e com duas nao:
// quem estiver conectado na instancia B nunca fica sabendo do que foi
// gravado na A. Como o gateway e ponto unico de falha da comunicacao da
// empresa (risco R6), instancia unica e um teto que tinha de cair.
//
// O desenho tem dois lados e uma regra:
//
//	registro.go  -- grava a linha em `evento`, DENTRO da transacao do fato
//	escutador.go -- escuta `gw_evento`, le a linha e publica no hub LOCAL
//
// A regra: ninguem mais publica no hub. Se um handler gravasse a linha E
// publicasse direto, a instancia que originou entregaria o mesmo evento
// duas vezes -- e so para quem enviou, que e o sintoma mais dificil de
// reproduzir em dev com uma instancia so (risco R3).
//
// A unica excecao e o chatinterno.Poller, e ela e deliberada: ver o
// comentario no proprio poller.
package eventos

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Registrador e o subconjunto de store.Queries necessario para publicar.
// E uma interface, e nao *store.Queries, porque quem publica as vezes e
// uma transacao (handlers) e as vezes o pool (outbox, webhook de status)
// -- e porque o outbox ja testa a orquestracao com uma fila falsa.
type Registrador interface {
	RegistrarEvento(ctx context.Context, arg store.RegistrarEventoParams) error
}

// Registrar enfileira o evento para as chaves "aplicacao:destino" dadas.
//
// Lista vazia grava a linha e nao entrega a ninguem -- o mesmo
// comportamento de sse.Hub.Publicar, de proposito: um evento sem
// destinatario listado nao sai, e nao vira broadcast.
func Registrar(ctx context.Context, r Registrador, chaves []string, evento sse.Evento) error {
	return gravar(ctx, r, chaves, nil, evento)
}

// RegistrarNaAplicacao entrega a TODOS os destinos de uma aplicacao.
//
// E o que sobrou do broadcast antigo; ver sse.Hub.PublicarNaAplicacao
// para por que ele ainda existe e quando some (fase 8).
func RegistrarNaAplicacao(ctx context.Context, r Registrador, app string, evento sse.Evento) error {
	return gravar(ctx, r, nil, &app, evento)
}

// RegistrarParaCorretorCRM mantem o contrato dos publicadores de WhatsApp
// (webhook, outbox, entregador), que conhecem um *int64 e nao uma chave.
//
// corretorID nulo e conversa ainda na fila de espera, sem dono: vira um
// EventoFilaAtualizada para a aplicacao do CRM. O tipo e outro de
// proposito -- retransmitir mensagem_nova faria a tela de conversa de
// cada corretor reagir a uma mensagem que nao e dele. Os campos originais
// sao descartados junto: quem esta na Fila so precisa saber que a lista
// mudou, e a lista ja e filtrada por permissao no CRM.
//
// Some na fase 8, junto com sse.ChaveCorretor e o caminho legado.
func RegistrarParaCorretorCRM(ctx context.Context, r Registrador, corretorID *int64, evento sse.Evento) error {
	if corretorID == nil {
		return RegistrarNaAplicacao(ctx, r, sse.AplicacaoCRM, sse.Evento{Tipo: sse.EventoFilaAtualizada, Caixa: evento.Caixa})
	}
	return Registrar(ctx, r, []string{sse.ChaveCorretor(*corretorID)}, evento)
}

func gravar(ctx context.Context, r Registrador, chaves []string, app *string, evento sse.Evento) error {
	payload, err := json.Marshal(evento)
	if err != nil {
		return fmt.Errorf("serializar evento %q: %w", evento.Tipo, err)
	}
	// chaves nulo viraria NULL numa coluna NOT NULL -- o array vazio e o
	// valor certo para "sem destinatario listado".
	if chaves == nil {
		chaves = []string{}
	}
	if err := r.RegistrarEvento(ctx, store.RegistrarEventoParams{
		Chaves:    chaves,
		Aplicacao: app,
		Payload:   payload,
	}); err != nil {
		return fmt.Errorf("registrar evento %q: %w", evento.Tipo, err)
	}
	return nil
}

// WhatsApp registra os eventos de WhatsApp (G8 de
// docs/PLANO_MULTICAIXA_E_CONVERSAS.md). Alem do caminho do CRM, entrega o
// evento completo a cada aplicacao de Aplicacoes, na chave
// "<app>:caixa:<caixa>" -- quem decide que um usuario pode ler a caixa e a
// aplicacao, ao emitir o token de sessao para esse destino.
//
// A caixa e a da conversa, passada a cada evento (G7): com mais de um
// numero, um valor fixo aqui entregaria o evento de uma caixa na chave da
// outra. Todo evento sai com o campo `caixa`, inclusive no caminho do CRM.
type WhatsApp struct {
	Aplicacoes []string
}

// DestinoCaixa e o destino opaco que a aplicacao pede em POST /v1/sessoes
// para receber os eventos de uma caixa.
func DestinoCaixa(caixa string) string { return "caixa:" + caixa }

func (w WhatsApp) Registrar(ctx context.Context, r Registrador, caixa string, corretorID *int64, evento sse.Evento) error {
	evento.Caixa = caixa
	if err := RegistrarParaCorretorCRM(ctx, r, corretorID, evento); err != nil {
		return err
	}
	if len(w.Aplicacoes) == 0 || caixa == "" {
		return nil
	}
	chaves := make([]string, 0, len(w.Aplicacoes))
	for _, app := range w.Aplicacoes {
		chaves = append(chaves, sse.ChaveDestino(app, DestinoCaixa(caixa)))
	}
	return Registrar(ctx, r, chaves, evento)
}
