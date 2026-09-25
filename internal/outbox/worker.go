// package outbox drena a fila de saida -- a tabela mensagem com
// status = 'pendente' e a propria fila (secao 7 do plano, sem tabela
// separada).
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/dlp"
	"github.com/LucasGardoni/whatsapp-gateway/internal/eventos"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Fila e o subconjunto de store.Queries que o outbox precisa. Definido
// aqui para testar a orquestracao sem depender de Postgres real.
type Fila interface {
	SelecionarPendentesParaEnvio(ctx context.Context, arg store.SelecionarPendentesParaEnvioParams) ([]store.SelecionarPendentesParaEnvioRow, error)
	MarcarMensagemEnviada(ctx context.Context, arg store.MarcarMensagemEnviadaParams) error
	MarcarMensagemParaRetentativa(ctx context.Context, arg store.MarcarMensagemParaRetentativaParams) error
	MarcarMensagemFalhaDefinitiva(ctx context.Context, arg store.MarcarMensagemFalhaDefinitivaParams) error
	MarcarMensagemBloqueada(ctx context.Context, id int64) error
	RegistrarOcorrenciaDLP(ctx context.Context, arg store.RegistrarOcorrenciaDLPParams) error
	ResetarMensagensPresasEmEnvio(ctx context.Context) error
	// o evento de tempo real e uma linha em `evento` como qualquer outra
	// escrita (fase 7) -- ver internal/eventos.
	eventos.Registrador
}

// Config parametriza o worker. Sao decisoes de operacao, nao de negocio --
// nao ha valor fechado no plano para elas, entao ficam com defaults
// sensatos e ajustaveis.
type Config struct {
	Intervalo     time.Duration
	TamanhoLote   int32
	MaxTentativas int
	TimeoutCiclo  time.Duration
	// MidiaDir e a raiz a que todo envio de midia fica confinado (P2-18).
	// Vazio cai no default "./dados/midia", igual ao config.MidiaDir --
	// nunca vira "leia de qualquer lugar do disco".
	MidiaDir string
	// EventosWhatsApp entrega os eventos de status tambem por caixa (G8).
	EventosWhatsApp eventos.WhatsApp
}

func (c Config) comDefaults() Config {
	if c.Intervalo <= 0 {
		c.Intervalo = 5 * time.Second
	}
	if c.TamanhoLote <= 0 {
		c.TamanhoLote = 20
	}
	if c.MaxTentativas <= 0 {
		c.MaxTentativas = 5
	}
	if c.TimeoutCiclo <= 0 {
		c.TimeoutCiclo = 30 * time.Second
	}
	if c.MidiaDir == "" {
		c.MidiaDir = "./dados/midia"
	}
	return c
}

// Worker drena a fila de cada caixa pelo provedor DELA (G1). Nao existe
// mais provedor do processo: uma mensagem so e selecionada junto com o
// provedor que vai manda-la, entao nao ha caminho em que a mensagem de uma
// caixa saia pelo numero de outra.
type Worker struct {
	fila   Fila
	caixas caixa.ComProvedores
	dlp    *dlp.Motor
	cfg    Config
}

func NovoWorker(fila Fila, caixas caixa.ComProvedores, motor *dlp.Motor, cfg Config) *Worker {
	return &Worker{fila: fila, caixas: caixas, dlp: motor, cfg: cfg.comDefaults()}
}

// Executar reseta mensagens presas de um restart anterior e entao roda um
// ciclo por tick ate o contexto ser cancelado. O ciclo em andamento
// sempre termina antes do worker parar -- ver rodarCiclo para como isso
// sobrevive ao proprio cancelamento do contexto de shutdown.
func (w *Worker) Executar(ctx context.Context) error {
	if err := w.fila.ResetarMensagensPresasEmEnvio(ctx); err != nil {
		return fmt.Errorf("outbox: resetar mensagens presas em envio: %w", err)
	}

	ticker := time.NewTicker(w.cfg.Intervalo)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// context.WithoutCancel: o ciclo ja em andamento nao deve ser
			// cortado pelo mesmo sinal que esta pedindo o shutdown --
			// senao uma mensagem fica presa em 'enviando' sem necessidade.
			// TimeoutCiclo garante que ele nao trave para sempre.
			cicloCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.TimeoutCiclo)
			if err := w.rodarCiclo(cicloCtx); err != nil {
				slog.Error("outbox: ciclo falhou", "erro", err)
			}
			cancel()
		}
	}
}

// rodarCiclo passa por cada caixa ativa. Caixa desconectada, ou cujo status
// nao responde, fica para o proximo ciclo sem segurar as outras: um numero
// banido nao pode parar a fila de outro.
func (w *Worker) rodarCiclo(ctx context.Context) error {
	caixas, err := w.caixas.Ativas(ctx)
	if err != nil {
		return fmt.Errorf("outbox: listar caixas: %w", err)
	}

	var falhas []error
	for _, c := range caixas {
		if err := w.rodarCaixa(ctx, c); err != nil {
			falhas = append(falhas, err)
		}
	}
	return errors.Join(falhas...)
}

func (w *Worker) rodarCaixa(ctx context.Context, c caixa.Caixa) error {
	p := w.caixas.Provedor(c)
	if p == nil {
		slog.Error("outbox: caixa sem provedor, fila parada", "caixa", c.Codigo)
		return nil
	}

	status, err := p.Status(ctx)
	if err != nil {
		slog.Warn("outbox: nao foi possivel consultar status do provedor, aguardando proximo ciclo", "caixa", c.Codigo, "erro", err)
		return nil
	}
	if !status.Conectada {
		// nao empurra para a fila da propria z-api (secao 4.7) -- so espera.
		slog.Warn("outbox: instancia desconectada, nao tenta enviar neste ciclo", "caixa", c.Codigo)
		return nil
	}

	mensagens, err := w.fila.SelecionarPendentesParaEnvio(ctx, store.SelecionarPendentesParaEnvioParams{
		CaixaID: c.ID,
		Limite:  w.cfg.TamanhoLote,
	})
	if err != nil {
		return fmt.Errorf("outbox: selecionar pendentes da caixa %s: %w", c.Codigo, err)
	}

	for _, m := range mensagens {
		w.processarMensagem(ctx, c, p, m)
	}
	return nil
}

func (w *Worker) processarMensagem(ctx context.Context, c caixa.Caixa, p provedor.Provedor, m store.SelecionarPendentesParaEnvioRow) {
	destino, err := destinatario(m)
	if err != nil {
		slog.Error("outbox: mensagem sem destinatario valido, falha definitiva", "mensagem_id", m.ID, "erro", err)
		w.marcarFalhaDefinitiva(ctx, c, m, err)
		return
	}

	// legenda vale tanto pro corpo do texto quanto pro caption de uma midia
	// -- dlp roda em qualquer um dos dois casos, antes de qualquer entrega
	// ao provedor. Nenhum caminho de envio pode contornar isso (secao 6,
	// diretriz 10).
	var legenda string
	if m.Texto != nil {
		legenda = *m.Texto
	}
	if legenda != "" {
		veredito := w.dlp.Avaliar(legenda)
		w.registrarOcorrenciasDLP(ctx, m.ID, veredito)
		if veredito.Bloqueado() {
			slog.Warn("outbox: mensagem bloqueada pelo dlp", "mensagem_id", m.ID)
			w.marcarBloqueada(ctx, c, m)
			return
		}
	}

	// erro de validacao (conteudo ausente, arquivo ilegivel): falha direto,
	// sem passar por tratarErroEnvio -- retentar nao muda nada aqui, o
	// mesmo tratamento que o destinatario invalido ja recebe acima.
	resultado, err, valido := w.enviar(ctx, p, m, destino, legenda)
	if !valido {
		slog.Error("outbox: mensagem invalida para envio, falha definitiva", "mensagem_id", m.ID, "erro", err)
		w.marcarFalhaDefinitiva(ctx, c, m, err)
		return
	}
	if err != nil {
		w.tratarErroEnvio(ctx, c, m, err)
		return
	}

	if err := w.fila.MarcarMensagemEnviada(ctx, store.MarcarMensagemEnviadaParams{
		ID:            m.ID,
		ProvedorMsgID: naoVazio(resultado.MessageID),
		ZaapID:        naoVazio(resultado.ZaapID),
	}); err != nil {
		slog.Error("outbox: falha ao marcar mensagem como enviada", "mensagem_id", m.ID, "erro", err)
		return
	}
	w.publicar(ctx, c, m, "enviada")
}

// enviar despacha para o endpoint certo conforme o tipo da mensagem.
// valido=false significa que o problema e da propria mensagem (conteudo
// ausente, arquivo ilegivel) e nunca vai se resolver com retentativa --
// dessa forma o chamador sabe falhar direto em vez de reagendar.
func (w *Worker) enviar(ctx context.Context, p provedor.Provedor, m store.SelecionarPendentesParaEnvioRow, destino, legenda string) (resultado *provedor.ResultadoEnvio, err error, valido bool) {
	if m.Tipo == "texto" {
		if legenda == "" {
			return nil, fmt.Errorf("mensagem %d: tipo texto sem conteudo", m.ID), false
		}
		resultado, err = p.Enviar(ctx, provedor.MensagemTexto{Destinatario: destino, Texto: legenda})
		return resultado, err, true
	}

	// midia: le o arquivo do disco (mesma pasta onde a recepcao grava
	// anexos recebidos, fase 4) e manda pelo endpoint z-api correspondente
	// ao tipo (fase 9 -- antes disso o outbox so sabia mandar texto).
	if m.MidiaCaminho == nil || *m.MidiaCaminho == "" {
		return nil, fmt.Errorf("mensagem %d: tipo %s sem midia_caminho", m.ID, m.Tipo), false
	}
	// confinado a MidiaDir: midia_caminho vem do CRUD de Admin do CRM e nao
	// e confiavel (P2-18). Caminho fora da raiz cai aqui como valido=false,
	// ou seja, falha definitiva -- retentar nunca vai permitir o caminho.
	conteudo, err := midia.CodificarBase64(w.cfg.MidiaDir, *m.MidiaCaminho)
	if err != nil {
		return nil, err, false
	}

	resultado, err = p.EnviarMidia(ctx, provedor.MensagemMidia{
		Destinatario:   destino,
		Tipo:           m.Tipo,
		ConteudoBase64: conteudo,
		NomeArquivo:    filepath.Base(*m.MidiaCaminho),
		Legenda:        legenda,
	})
	return resultado, err, true
}

func (w *Worker) tratarErroEnvio(ctx context.Context, c caixa.Caixa, m store.SelecionarPendentesParaEnvioRow, err error) {
	retentavel := true // erro nao classificado: mais seguro reenfileirar do que descartar
	var classificado provedor.ErroClassificado
	if errors.As(err, &classificado) {
		retentavel = classificado.Retentavel()
	}

	tentativasApos := int(m.Tentativas) + 1
	if !retentavel || tentativasApos >= w.cfg.MaxTentativas {
		slog.Error("outbox: falha definitiva no envio", "caixa", c.Codigo, "mensagem_id", m.ID, "tentativas", tentativasApos, "erro", err)
		w.marcarFalhaDefinitiva(ctx, c, m, err)
		return
	}

	// o atraso vai como intervalo e o tentar_em e calculado no relogio do
	// banco (ver outbox.sql) -- e o banco que compara essa coluna depois.
	if err := w.fila.MarcarMensagemParaRetentativa(ctx, store.MarcarMensagemParaRetentativaParams{
		ID:             m.ID,
		AtrasoSegundos: calcularBackoff(int(m.Tentativas)).Seconds(),
		UltimoErro:     naoVazio(motivoErro(err)),
	}); err != nil {
		slog.Error("outbox: falha ao reagendar mensagem", "mensagem_id", m.ID, "erro", err)
	}
}

func (w *Worker) marcarFalhaDefinitiva(ctx context.Context, c caixa.Caixa, m store.SelecionarPendentesParaEnvioRow, causa error) {
	if err := w.fila.MarcarMensagemFalhaDefinitiva(ctx, store.MarcarMensagemFalhaDefinitivaParams{
		ID:         m.ID,
		UltimoErro: naoVazio(motivoErro(causa)),
	}); err != nil {
		slog.Error("outbox: falha ao marcar falha definitiva", "mensagem_id", m.ID, "erro", err)
		return
	}
	w.publicar(ctx, c, m, "falha")
}

// motivoErro limita o tamanho gravado em mensagem.ultimo_erro -- o texto
// do erro pode incluir o corpo inteiro da resposta da z-api (ver ErroEnvio),
// e isso nao precisa virar uma coluna gigante (fase 9).
func motivoErro(err error) string {
	const tamanhoMaximo = 500
	msg := err.Error()
	if len(msg) > tamanhoMaximo {
		return msg[:tamanhoMaximo]
	}
	return msg
}

func (w *Worker) marcarBloqueada(ctx context.Context, c caixa.Caixa, m store.SelecionarPendentesParaEnvioRow) {
	if err := w.fila.MarcarMensagemBloqueada(ctx, m.ID); err != nil {
		slog.Error("outbox: falha ao marcar mensagem bloqueada", "mensagem_id", m.ID, "erro", err)
		return
	}
	w.publicar(ctx, c, m, "bloqueada")
}

// publicar registra a mudanca de status para o tempo real. Retentativa
// (status continua 'pendente') nao publica: nao muda nada que a tela
// precise refletir ainda.
//
// Desde a fase 7 isto grava em `evento` em vez de publicar no hub local:
// o corretor pode estar com o EventSource aberto em outra instancia, e
// era exatamente esse o teto que a fase derruba.
func (w *Worker) publicar(ctx context.Context, c caixa.Caixa, m store.SelecionarPendentesParaEnvioRow, status string) {
	if err := w.cfg.EventosWhatsApp.Registrar(ctx, w.fila, c.Codigo, m.CorretorID, sse.Evento{
		Tipo:       sse.EventoMensagemStatus,
		MensagemID: m.ID,
		ConversaID: m.ConversaID,
		Status:     status,
	}); err != nil {
		slog.Error("outbox: registrar evento de status", "mensagem_id", m.ID, "status", status, "erro", err)
	}
}

// registrarOcorrenciasDLP grava avisar/bloquear para o relatorio do
// supervisor (secao 6). Liberar so vai pro log -- nao precisa de auditoria.
func (w *Worker) registrarOcorrenciasDLP(ctx context.Context, mensagemID int64, veredito dlp.Resultado) {
	for _, oc := range veredito.Ocorrencias {
		if oc.Decisao == dlp.Liberar {
			slog.Info("outbox: dlp liberou ocorrencia", "mensagem_id", mensagemID, "regra", oc.Regra)
			continue
		}
		if err := w.fila.RegistrarOcorrenciaDLP(ctx, store.RegistrarOcorrenciaDLPParams{
			MensagemID: mensagemID,
			Regra:      oc.Regra,
			Decisao:    string(oc.Decisao),
			Confianca:  confiancaParaNumeric(oc.Confianca),
		}); err != nil {
			slog.Error("outbox: falha ao registrar ocorrencia dlp", "mensagem_id", mensagemID, "regra", oc.Regra, "erro", err)
		}
	}
}

func confiancaParaNumeric(v float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.2f", v))
	return n
}

// destinatario prefere chat_lid -- e a identidade primaria do contato
// (secao 4.3). Cai para telefone_e164 so quando o lead ainda nao tem lid
// resolvido.
func destinatario(m store.SelecionarPendentesParaEnvioRow) (string, error) {
	if m.ChatLid != nil && *m.ChatLid != "" {
		return *m.ChatLid, nil
	}
	if m.TelefoneE164 != nil && *m.TelefoneE164 != "" {
		return *m.TelefoneE164, nil
	}
	return "", fmt.Errorf("mensagem %d: lead sem chat_lid e sem telefone_e164", m.ID)
}

// calcularBackoff cresce exponencialmente a partir de 30s, com teto de
// 30 minutos. tentativasAnteriores conta so as tentativas ja feitas antes
// desta falha.
func calcularBackoff(tentativasAnteriores int) time.Duration {
	const base = 30 * time.Second
	const teto = 30 * time.Minute

	d := base << tentativasAnteriores
	if d <= 0 || d > teto { // overflow de shift tambem cai aqui
		return teto
	}
	return d
}

func naoVazio(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
