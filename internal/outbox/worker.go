// package outbox drena a fila de saida -- a tabela mensagem com
// status = 'pendente' e a propria fila (secao 7 do plano, sem tabela
// separada).
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Fila e o subconjunto de store.Queries que o outbox precisa. Definido
// aqui para testar a orquestracao sem depender de Postgres real.
type Fila interface {
	SelecionarPendentesParaEnvio(ctx context.Context, limite int32) ([]store.SelecionarPendentesParaEnvioRow, error)
	MarcarMensagemEnviada(ctx context.Context, arg store.MarcarMensagemEnviadaParams) error
	MarcarMensagemParaRetentativa(ctx context.Context, arg store.MarcarMensagemParaRetentativaParams) error
	MarcarMensagemFalhaDefinitiva(ctx context.Context, id int64) error
	ResetarMensagensPresasEmEnvio(ctx context.Context) error
}

// Config parametriza o worker. Sao decisoes de operacao, nao de negocio --
// nao ha valor fechado no plano para elas, entao ficam com defaults
// sensatos e ajustaveis.
type Config struct {
	Intervalo     time.Duration
	TamanhoLote   int32
	MaxTentativas int
	TimeoutCiclo  time.Duration
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
	return c
}

type Worker struct {
	fila     Fila
	provedor provedor.Provedor
	cfg      Config
}

func NovoWorker(fila Fila, p provedor.Provedor, cfg Config) *Worker {
	return &Worker{fila: fila, provedor: p, cfg: cfg.comDefaults()}
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

func (w *Worker) rodarCiclo(ctx context.Context) error {
	status, err := w.provedor.Status(ctx)
	if err != nil {
		slog.Warn("outbox: nao foi possivel consultar status do provedor, aguardando proximo ciclo", "erro", err)
		return nil
	}
	if !status.Conectada {
		// nao empurra para a fila da propria z-api (secao 4.7) -- so espera.
		slog.Warn("outbox: instancia desconectada, nao tenta enviar neste ciclo")
		return nil
	}

	mensagens, err := w.fila.SelecionarPendentesParaEnvio(ctx, w.cfg.TamanhoLote)
	if err != nil {
		return fmt.Errorf("outbox: selecionar pendentes: %w", err)
	}

	for _, m := range mensagens {
		w.processarMensagem(ctx, m)
	}
	return nil
}

func (w *Worker) processarMensagem(ctx context.Context, m store.SelecionarPendentesParaEnvioRow) {
	destino, err := destinatario(m)
	if err != nil {
		slog.Error("outbox: mensagem sem destinatario valido, falha definitiva", "mensagem_id", m.ID, "erro", err)
		w.marcarFalhaDefinitiva(ctx, m.ID)
		return
	}

	if m.Texto == nil {
		slog.Error("outbox: mensagem sem texto, envio de midia ainda nao suportado", "mensagem_id", m.ID)
		w.marcarFalhaDefinitiva(ctx, m.ID)
		return
	}

	resultado, err := w.provedor.Enviar(ctx, provedor.MensagemTexto{Destinatario: destino, Texto: *m.Texto})
	if err != nil {
		w.tratarErroEnvio(ctx, m, err)
		return
	}

	if err := w.fila.MarcarMensagemEnviada(ctx, store.MarcarMensagemEnviadaParams{
		ID:            m.ID,
		ProvedorMsgID: naoVazio(resultado.MessageID),
		ZaapID:        naoVazio(resultado.ZaapID),
	}); err != nil {
		slog.Error("outbox: falha ao marcar mensagem como enviada", "mensagem_id", m.ID, "erro", err)
	}
}

func (w *Worker) tratarErroEnvio(ctx context.Context, m store.SelecionarPendentesParaEnvioRow, err error) {
	retentavel := true // erro nao classificado: mais seguro reenfileirar do que descartar
	var classificado provedor.ErroClassificado
	if errors.As(err, &classificado) {
		retentavel = classificado.Retentavel()
	}

	tentativasApos := int(m.Tentativas) + 1
	if !retentavel || tentativasApos >= w.cfg.MaxTentativas {
		slog.Error("outbox: falha definitiva no envio", "mensagem_id", m.ID, "tentativas", tentativasApos, "erro", err)
		w.marcarFalhaDefinitiva(ctx, m.ID)
		return
	}

	tentarEm := time.Now().Add(calcularBackoff(int(m.Tentativas)))
	if err := w.fila.MarcarMensagemParaRetentativa(ctx, store.MarcarMensagemParaRetentativaParams{
		ID:       m.ID,
		TentarEm: pgtype.Timestamp{Time: tentarEm, Valid: true},
	}); err != nil {
		slog.Error("outbox: falha ao reagendar mensagem", "mensagem_id", m.ID, "erro", err)
	}
}

func (w *Worker) marcarFalhaDefinitiva(ctx context.Context, id int64) {
	if err := w.fila.MarcarMensagemFalhaDefinitiva(ctx, id); err != nil {
		slog.Error("outbox: falha ao marcar falha definitiva", "mensagem_id", id, "erro", err)
	}
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
