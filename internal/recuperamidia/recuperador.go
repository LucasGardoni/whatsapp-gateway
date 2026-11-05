// package recuperamidia baixa de novo a midia recebida que falhou na chegada
// (migration 00033). O servidor de arquivos da z-api oscila: o download na
// hora do webhook as vezes da timeout e, sem nova tentativa, a imagem ficava
// indisponivel mesmo com o link ainda valendo por 30 dias.
package recuperamidia

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/auditoria"
	"github.com/LucasGardoni/whatsapp-gateway/internal/eventos"
	"github.com/LucasGardoni/whatsapp-gateway/internal/midia"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// AcaoMidia e a acao do elo em mensagem_alteracao: o arquivo chegou depois.
const AcaoMidia = "midia"

// autorSistema assina o elo: ninguem pediu, o gateway recuperou sozinho.
const autorSistema = "sistema"

type Config struct {
	// Intervalo entre passadas.
	Intervalo time.Duration
	// Lote e quantas pendencias vencidas cada passada tenta.
	Lote int32
	// MaxTentativas antes de desistir. Com espera dobrando a partir de 1 min
	// e teto de 6 h, 15 tentativas cobrem uns 3 dias.
	MaxTentativas int32
	// EsperaInicial e EsperaMaxima limitam o recuo entre tentativas.
	EsperaInicial time.Duration
	EsperaMaxima  time.Duration
	// EventosWhatsApp avisa a tela aberta para reler a mensagem.
	EventosWhatsApp eventos.WhatsApp
}

func (c Config) comDefaults() Config {
	if c.Intervalo <= 0 {
		c.Intervalo = time.Minute
	}
	if c.Lote <= 0 {
		c.Lote = 20
	}
	if c.MaxTentativas <= 0 {
		c.MaxTentativas = 15
	}
	if c.EsperaInicial <= 0 {
		c.EsperaInicial = time.Minute
	}
	if c.EsperaMaxima <= 0 {
		c.EsperaMaxima = 6 * time.Hour
	}
	return c
}

type Recuperador struct {
	pool     *pgxpool.Pool
	baixador *midia.Baixador
	cfg      Config
}

func Novo(pool *pgxpool.Pool, baixador *midia.Baixador, cfg Config) *Recuperador {
	return &Recuperador{pool: pool, baixador: baixador, cfg: cfg.comDefaults()}
}

// Executar roda uma passada na subida e depois uma por tick.
func (r *Recuperador) Executar(ctx context.Context) error {
	r.passar(ctx)

	ticker := time.NewTicker(r.cfg.Intervalo)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.passar(ctx)
		}
	}
}

// passar nunca devolve erro: falha aqui nao pode derrubar o gateway.
func (r *Recuperador) passar(ctx context.Context) {
	pendentes, err := store.New(r.pool).ListarMidiasPendentesVencidas(ctx, r.cfg.Lote)
	if err != nil {
		slog.Error("recupera midia: listar pendentes", "erro", err)
		return
	}
	for _, p := range pendentes {
		if ctx.Err() != nil {
			return
		}
		r.tentar(ctx, p)
	}
}

func (r *Recuperador) tentar(ctx context.Context, p store.ListarMidiasPendentesVencidasRow) {
	nomeBase := deref(p.ProvedorMsgID)
	if nomeBase == "" {
		r.desistir(ctx, p, "mensagem sem provedor_msg_id")
		return
	}

	// o download fica fora da transacao: rede lenta nao segura conexao do banco.
	caminho, err := r.baixador.Baixar(ctx, p.Url, nomeBase)
	if err != nil {
		var status midia.ErroStatus
		if errors.As(err, &status) && status.Definitivo() {
			r.desistir(ctx, p, err.Error())
			return
		}
		if p.Tentativas+1 >= r.cfg.MaxTentativas {
			r.desistir(ctx, p, err.Error())
			return
		}
		erro := err.Error()
		if err := store.New(r.pool).AdiarMidiaPendente(ctx, store.AdiarMidiaPendenteParams{
			MensagemID:     p.MensagemID,
			EsperaSegundos: r.espera(p.Tentativas).Seconds(),
			UltimoErro:     &erro,
		}); err != nil {
			slog.Error("recupera midia: adiar", "mensagem_id", p.MensagemID, "erro", err)
		}
		slog.Info("recupera midia: tentativa falhou", "mensagem_id", p.MensagemID, "tentativa", p.Tentativas+1, "erro", erro)
		return
	}

	if err := r.gravar(ctx, p, caminho); err != nil {
		slog.Error("recupera midia: gravar", "mensagem_id", p.MensagemID, "erro", err)
		return
	}
	slog.Info("recupera midia: recuperada", "mensagem_id", p.MensagemID, "tentativa", p.Tentativas+1)
}

// gravar registra o arquivo na mensagem, com elo na cadeia de hash, e avisa
// a tela -- tudo na mesma transacao.
func (r *Recuperador) gravar(ctx context.Context, p store.ListarMidiasPendentesVencidasRow, caminho string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)

	// outra instancia do gateway ja recuperou: o arquivo e o mesmo, nada a gravar.
	if _, err := q.TravarMidiaPendente(ctx, p.MensagemID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}

	alteracao := auditoria.Alteracao{MensagemID: p.MensagemID, Acao: AcaoMidia, Texto: caminho, Autor: autorSistema}
	linha, err := q.InserirAlteracaoMensagem(ctx, store.InserirAlteracaoMensagemParams{
		MensagemID: p.MensagemID,
		Acao:       AcaoMidia,
		Texto:      &caminho,
		Autor:      autorSistema,
	})
	if err != nil {
		return err
	}
	alteracao.ID = linha.ID
	if err := auditoria.RegistrarHashAlteracao(ctx, q, linha.ID, auditoria.CamposAlteracao(alteracao)...); err != nil {
		return err
	}

	if err := q.DefinirMidiaRecuperada(ctx, store.DefinirMidiaRecuperadaParams{ID: p.MensagemID, MidiaRecuperada: &caminho}); err != nil {
		return err
	}
	if err := q.RemoverMidiaPendente(ctx, p.MensagemID); err != nil {
		return err
	}

	// mensagem_editada faz a tela reler a conversa; editada_em nao muda, entao
	// a mensagem nao ganha o selo de editada.
	if err := r.cfg.EventosWhatsApp.Registrar(ctx, q, p.CaixaCodigo, p.CorretorID, sse.Evento{
		Tipo:       sse.EventoMensagemEditada,
		MensagemID: p.MensagemID,
		ConversaID: p.ConversaID,
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *Recuperador) desistir(ctx context.Context, p store.ListarMidiasPendentesVencidasRow, motivo string) {
	slog.Warn("recupera midia: desistindo", "mensagem_id", p.MensagemID, "tentativas", p.Tentativas+1, "motivo", motivo)
	if err := store.New(r.pool).RemoverMidiaPendente(ctx, p.MensagemID); err != nil {
		slog.Error("recupera midia: remover pendente", "mensagem_id", p.MensagemID, "erro", err)
	}
}

// espera dobra a cada tentativa, ate o teto.
func (r *Recuperador) espera(tentativas int32) time.Duration {
	d := time.Duration(float64(r.cfg.EsperaInicial) * math.Pow(2, float64(tentativas)))
	if d <= 0 || d > r.cfg.EsperaMaxima {
		return r.cfg.EsperaMaxima
	}
	return d
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
