package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/auditoria"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/mensagem"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Entregador e o que difere entre WhatsApp e interno (barramento, fase 6).
//
// Tudo antes dele -- autenticacao, decodificacao, transacao, commit e
// publicacao no hub -- e comum e vive em entregar(). O que sobra para cada
// canal e exatamente a saida, que a secao 3 do plano diz ser irredutivel:
// um tem DLP, outbox, retry e oito estados de status; o outro tem destino
// opaco, nenhuma fila e nada que mude depois.
//
// A escolha do Entregador e por `canal`, um DADO da requisicao. Nao existe
// e nao pode passar a existir aqui um ramo por aplicacao (secao 2, item
// 6): o aceite da fase e uma comparacao de codigo de aplicacao nao
// aparecer em lugar nenhum sob internal/ -- nem aqui, nem em comentario,
// que e por que esta frase nao escreve o operador.
type Entregador interface {
	// Validar roda antes de abrir transacao -- payload recusado nao custa
	// conexao de banco.
	Validar(req mensagem.Requisicao) error
	// Persistir grava a mensagem E o elo de auditoria na MESMA transacao
	// do chamador. As duas cadeias sao separadas (fase 5, decisao 2), e e
	// por isso que o hash entra aqui e nao no caminho comum.
	Persistir(ctx context.Context, q *store.Queries, app *middleware.Aplicacao, req mensagem.Requisicao) (Entrega, error)
}

// Entrega e o resultado de Persistir.
//
// Publicar substitui o Notificar(ctx, id) que o plano previa na interface:
// notificar depois do commit precisa saber PARA QUEM, e no canal interno
// essa lista tem de ser lida dentro da transacao (fase 5, decisao 7) --
// depois do commit ja e tarde, quem acabou de sair do canal ainda
// receberia. Uma closure fechada dentro de Persistir carrega a lista sem
// que o caminho comum precise conhecer nenhum dos dois formatos de destino.
//
// Publicar nulo e valido: e "gravou e nao ha ninguem a avisar".
type Entrega struct {
	ID       int64
	Status   string
	Publicar func(*sse.Hub)
}

// entregar e o caminho comum dos dois canais, e o unico lugar que abre a
// transacao de escrita de mensagem.
//
// app nulo e a rota legada POST /api/mensagens autenticada pelo
// GATEWAY_SERVICE_TOKEN, que nao identifica aplicacao -- some na fase 8.
// Em /v1/* ele nunca e nulo.
func entregar(
	ctx context.Context,
	pool *pgxpool.Pool,
	hub *sse.Hub,
	e Entregador,
	app *middleware.Aplicacao,
	req mensagem.Requisicao,
) (Entrega, error) {
	if err := e.Validar(req); err != nil {
		return Entrega{}, err
	}

	// tx envolve a mensagem e o elo de auditoria -- os dois confirmam
	// juntos, senao um crash entre eles deixa a mensagem fora da cadeia.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Entrega{}, fmt.Errorf("iniciar transacao: %w", err)
	}
	defer tx.Rollback(ctx)

	entrega, err := e.Persistir(ctx, store.New(tx), app, req)
	if err != nil {
		return Entrega{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Entrega{}, fmt.Errorf("commit: %w", err)
	}

	// publica so depois do commit: ninguem pode ser avisado de uma
	// mensagem que a transacao acabou descartando. Na fase 7 esta
	// publicacao direta sai e a fonte unica passa a ser o pg_notify --
	// manter as duas duplicaria o evento para quem enviou.
	if hub != nil && entrega.Publicar != nil {
		entrega.Publicar(hub)
	}
	return entrega, nil
}

// erroResposta e a recusa que o Entregador ja sabe traduzir em HTTP --
// "conversa encerrada" e 409 e nao 500, e so quem persiste sabe disso.
// Qualquer outro erro e falha do gateway e vira 500 com log.
type erroResposta struct {
	status int
	motivo string
}

func (e erroResposta) Error() string { return e.motivo }

func recusa(status int, motivo string) error { return erroResposta{status: status, motivo: motivo} }

// responderErro traduz o erro de entregar() em resposta. Motivo de recusa
// vai inteiro para quem integra; falha de gateway nao vaza nada e vira
// linha de log.
func responderErro(w http.ResponseWriter, err error, contexto ...any) {
	var recusado erroResposta
	switch {
	case errors.As(err, &recusado):
		http.Error(w, recusado.motivo, recusado.status)
	case mensagem.EhValidacao(err):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		slog.Error("mensagens: entrega falhou", append(contexto, "erro", err)...)
		http.Error(w, "erro interno", http.StatusInternalServerError)
	}
}

// entregadorWhatsApp reaproveita o caminho de sempre: mensagem em
// 'pendente', entregue depois pelo outbox. Nao existe aqui atalho para o
// provedor -- quem contorna o outbox contorna o DLP junto.
type entregadorWhatsApp struct {
	// midiaDir e a raiz a que todo midia_caminho fica confinado (P2-18).
	// Validado ja na entrada, e nao so na hora do envio, pra o corretor
	// receber o erro na hora em vez de a mensagem morrer no outbox.
	midiaDir string
}

func (e entregadorWhatsApp) Validar(req mensagem.Requisicao) error {
	msg := req.ComoWhatsApp()
	if err := msg.Validar(); err != nil {
		return err
	}
	if msg.MidiaCaminho == "" {
		return nil
	}
	if _, err := midia.ResolverDentroDe(e.midiaDir, msg.MidiaCaminho); err != nil {
		slog.Warn("mensagens: midia_caminho recusado", "conversa_id", msg.ConversaID, "erro", err)
		return recusa(http.StatusBadRequest, "midia_caminho fora do diretorio de midia permitido")
	}
	return nil
}

func (e entregadorWhatsApp) Persistir(ctx context.Context, q *store.Queries, app *middleware.Aplicacao, req mensagem.Requisicao) (Entrega, error) {
	msg := req.ComoWhatsApp()

	conversa, err := q.BuscarConversaPorID(ctx, msg.ConversaID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entrega{}, recusa(http.StatusNotFound, "conversa nao encontrada")
	}
	if err != nil {
		return Entrega{}, fmt.Errorf("buscar conversa %d: %w", msg.ConversaID, err)
	}
	if conversa.FechadaEm.Valid {
		return Entrega{}, recusa(http.StatusConflict, "conversa encerrada")
	}

	// procedencia (barramento, fase 1): sai do token autenticado, nunca do
	// corpo. Nulo quando a chamada veio pelo token legado, que nao
	// identifica aplicacao -- some na fase 8, junto com o token legado.
	var aplicacaoID *int64
	var origem string
	if app != nil {
		aplicacaoID = &app.ID
		origem = app.Codigo
	}

	linha, err := q.CriarMensagemSaida(ctx, store.CriarMensagemSaidaParams{
		ConversaID:   msg.ConversaID,
		Tipo:         msg.Tipo,
		Texto:        naoVazio(msg.Texto),
		MidiaCaminho: naoVazio(msg.MidiaCaminho),
		AplicacaoID:  aplicacaoID,
	})
	if err != nil {
		return Entrega{}, fmt.Errorf("criar mensagem de saida: %w", err)
	}

	// o hash cobre tipo e caminho reais, nao "texto"/"" fixos: senao duas
	// mensagens com a mesma legenda e arquivos diferentes teriam o mesmo
	// elo, e a cadeia deixaria de provar o que foi de fato enviado.
	//
	// Origem entra na cadeia a partir da fase 2 do barramento: a trilha
	// passa a provar quem originou, nao so o que foi dito.
	if err := auditoria.RegistrarHash(ctx, q, linha.ID,
		auditoria.CamposMensagem(auditoria.Mensagem{
			ID:           linha.ID,
			ConversaID:   linha.ConversaID,
			Direcao:      "saida",
			Tipo:         msg.Tipo,
			Texto:        msg.Texto,
			MidiaCaminho: msg.MidiaCaminho,
			Origem:       origem,
		})...,
	); err != nil {
		return Entrega{}, fmt.Errorf("registrar hash de auditoria da mensagem %d: %w", linha.ID, err)
	}

	corretorID := conversa.CorretorID
	return Entrega{
		ID:     linha.ID,
		Status: linha.Status,
		Publicar: func(hub *sse.Hub) {
			hub.PublicarParaCorretorCRM(corretorID, sse.Evento{
				Tipo:       sse.EventoMensagemNova,
				MensagemID: linha.ID,
				ConversaID: linha.ConversaID,
				Status:     linha.Status,
			})
		},
	}, nil
}

// entregadorInterno grava o blob que o gateway NAO consegue abrir -- a
// chave fica no backend da aplicacao e nunca transita por aqui (secao 4).
// Sem DLP (nao ha o que ler), sem outbox e sem status que mude depois.
type entregadorInterno struct{}

func (entregadorInterno) Validar(req mensagem.Requisicao) error {
	msg, err := req.ComoInterna()
	if err != nil {
		return err
	}
	return msg.Validar()
}

func (entregadorInterno) Persistir(ctx context.Context, q *store.Queries, app *middleware.Aplicacao, req mensagem.Requisicao) (Entrega, error) {
	if app == nil {
		// canal interno e escopado por aplicacao do inicio ao fim: sem
		// aplicacao nao ha canal a que a mensagem pertenca. Inalcancavel
		// por /v1/*, que e estrito -- fica como defesa em profundidade
		// para o dia em que alguem montar a rota em outro lugar.
		return Entrega{}, recusa(http.StatusUnauthorized, "nao autorizado")
	}

	msg, err := req.ComoInterna()
	if err != nil {
		return Entrega{}, err
	}

	linha, err := q.CriarMensagemInterna(ctx, store.CriarMensagemInternaParams{
		AplicacaoID:      app.ID,
		CanalExterno:     msg.CanalExterno,
		RemetenteExterno: &msg.Remetente,
		ConteudoCifrado:  msg.ConteudoCifrado,
		CifraAlg:         &msg.CifraAlg,
		CifraVersao:      &msg.CifraVersao,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// o INSERT ... SELECT nao acha canal: ou nao existe, ou e de outra
		// aplicacao. As duas respondem igual, de proposito -- distinguir
		// confirmaria a existencia do canal alheio.
		return Entrega{}, recusa(http.StatusNotFound, "canal nao encontrado")
	}
	if err != nil {
		return Entrega{}, fmt.Errorf("criar mensagem interna: %w", err)
	}

	// canal_ref_id nunca volta nulo aqui: a linha acabou de ser inserida a
	// partir de uma linha de canal. Conferido mesmo assim porque a
	// alternativa seria desreferenciar e derrubar o processo.
	if linha.CanalRefID == nil {
		return Entrega{}, fmt.Errorf("mensagem interna %d gravada sem canal", linha.ID)
	}
	canalID := *linha.CanalRefID

	// a cadeia encadeia o CIPHERTEXT (secao 4, "Auditoria sob cifra"):
	// continua provando ordem e integridade, e deixa de provar conteudo
	// sem a chave da aplicacao -- que e o esperado no modelo (a), nao uma
	// perda acidental.
	if err := auditoria.RegistrarHashInterna(ctx, q, linha.ID,
		auditoria.CamposMensagemInterna(auditoria.MensagemInterna{
			ID:              linha.ID,
			CanalID:         canalID,
			Remetente:       msg.Remetente,
			CifraAlg:        msg.CifraAlg,
			CifraVersao:     msg.CifraVersao,
			ConteudoCifrado: msg.ConteudoCifrado,
			Origem:          app.Codigo,
		})...,
	); err != nil {
		return Entrega{}, fmt.Errorf("registrar hash da mensagem interna %d: %w", linha.ID, err)
	}

	// a lista de entrega e lida DENTRO da transacao: quem foi removido do
	// canal no mesmo instante nao deve receber, e ler depois do commit
	// abriria essa janela.
	chaves, err := q.ListarChavesDeEntregaDoCanal(ctx, canalID)
	if err != nil {
		return Entrega{}, fmt.Errorf("listar destinos do canal %d: %w", canalID, err)
	}

	canalExterno := msg.CanalExterno
	return Entrega{
		ID: linha.ID,
		// "registrada", e nao "pendente" como no WhatsApp: 'pendente' e o
		// primeiro estado de uma maquina de entrega que o canal interno
		// nao tem. Divergencia consciente de 6.1, registrada na fase 5.
		Status: "registrada",
		Publicar: func(hub *sse.Hub) {
			hub.Publicar(chaves, sse.Evento{
				Tipo:         sse.EventoMensagemInternaNova,
				MensagemID:   linha.ID,
				CanalExterno: canalExterno,
			})
		},
	}, nil
}
