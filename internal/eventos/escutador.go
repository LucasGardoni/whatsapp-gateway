package eventos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// canalNotify e o canal de LISTEN/NOTIFY. Um so para todos os tipos de
// evento: o roteamento esta na linha, nao no nome do canal.
const canalNotify = "gw_evento"

// Leitor e o subconjunto de store.Queries que o escutador consome.
type Leitor interface {
	BuscarUltimoIDEvento(ctx context.Context) (int64, error)
	BuscarEventoPorID(ctx context.Context, id int64) (store.Evento, error)
	ListarEventosAPartirDe(ctx context.Context, id int64) ([]store.Evento, error)
	LimparEventosAntigos(ctx context.Context, idade pgtype.Interval) (int64, error)
}

type Config struct {
	// Reconciliacao e a terceira camada (secao 7.2, item 3): mesmo sem
	// queda de conexao, uma varredura periodica pega o que por qualquer
	// motivo nao chegou pelo NOTIFY.
	Reconciliacao time.Duration
	// BackoffMin e BackoffMax limitam a reconexao. Enquanto a conexao
	// dedicada esta caida o tempo real desta instancia depende so da
	// reconciliacao, entao o minimo e curto.
	BackoffMin time.Duration
	BackoffMax time.Duration
	// Retencao e a idade a partir da qual um evento e apagado. Evento e
	// roteamento, nao historico -- ver a query LimparEventosAntigos.
	Retencao time.Duration
	// MargemCommit e o quanto o cursor de catch-up fica para tras do maior
	// id ja visto. Ver varrer.
	MargemCommit time.Duration
}

func (c Config) comDefaults() Config {
	if c.Reconciliacao <= 0 {
		c.Reconciliacao = 60 * time.Second
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.Retencao <= 0 {
		c.Retencao = 24 * time.Hour
	}
	if c.MargemCommit <= 0 {
		c.MargemCommit = 30 * time.Second
	}
	return c
}

// Escutador transforma as linhas de `evento` em entregas no hub LOCAL
// desta instancia.
//
// Duas camadas de entrega, e elas nao sao redundancia de luxo:
//
//	rapida -- o NOTIFY traz o id e uma consulta traz a linha. E o caminho
//	          de praticamente todo evento, e o que faz caber em menos de 1s.
//	varrer -- consulta `id > ultimoID`. Roda na reconexao e a cada
//	          Reconciliacao. NOTIFY perdido durante uma queda NAO e
//	          reentregue pelo Postgres (secao 7.2, item 2); sem esta
//	          camada o evento simplesmente sumiria.
//
// entregues evita que as duas camadas entreguem a mesma linha duas vezes.
type Escutador struct {
	dsn    string
	leitor Leitor
	hub    *sse.Hub
	cfg    Config

	// ultimoID e o cursor de catch-up. So varrer o move -- ver la por que
	// o caminho rapido NAO pode move-lo.
	ultimoID int64
	// entregues sao os ids ja publicados que o cursor ainda nao cobre.
	// Podado a cada varredura, entao guarda no maximo o trafego de uma
	// janela de reconciliacao.
	entregues map[int64]struct{}
}

func NovoEscutador(dsn string, leitor Leitor, hub *sse.Hub, cfg Config) *Escutador {
	return &Escutador{
		dsn:       dsn,
		leitor:    leitor,
		hub:       hub,
		cfg:       cfg.comDefaults(),
		entregues: make(map[int64]struct{}),
	}
}

// Executar roda ate o contexto ser cancelado. So devolve erro no que nao
// adianta repetir: queda de conexao e tratada com backoff aqui dentro,
// porque uma instancia que morre porque o banco piscou deixa de atender
// HTTP junto.
func (e *Escutador) Executar(ctx context.Context) error {
	ultimo, err := e.leitor.BuscarUltimoIDEvento(ctx)
	if err != nil {
		return fmt.Errorf("eventos: cursor inicial: %w", err)
	}
	e.ultimoID = ultimo

	// a goroutine de conexao so traduz notificacao em id; quem consulta o
	// banco e publica e o laco abaixo, para que uma consulta lenta nao
	// segure a conexao dedicada de LISTEN.
	notificacoes := make(chan int64, 64)
	reconectou := make(chan struct{}, 1)
	go e.escutar(ctx, notificacoes, reconectou)

	ticker := time.NewTicker(e.cfg.Reconciliacao)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case id := <-notificacoes:
			e.entregarPorID(ctx, id)
		case <-reconectou:
			// catch-up obrigatorio: o que aconteceu enquanto a conexao
			// estava caida nao gerou notificacao nenhuma para esta instancia.
			e.varrer(ctx)
		case <-ticker.C:
			e.varrer(ctx)
			e.limpar(ctx)
		}
	}
}

// escutar mantem a conexao DEDICADA de LISTEN. Dedicada e fora do
// pgxpool: uma conexao de pool volta para o pool e o LISTEN morre junto,
// em silencio -- o gateway continuaria de pe e simplesmente pararia de
// entregar tempo real.
func (e *Escutador) escutar(ctx context.Context, notificacoes chan<- int64, reconectou chan<- struct{}) {
	espera := e.cfg.BackoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		err := e.cicloDeConexao(ctx, notificacoes, reconectou)
		if ctx.Err() != nil {
			return
		}
		slog.Error("eventos: conexao de LISTEN caiu, reconectando", "espera", espera, "erro", err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(espera):
		}
		if espera *= 2; espera > e.cfg.BackoffMax {
			espera = e.cfg.BackoffMax
		}
	}
}

func (e *Escutador) cicloDeConexao(ctx context.Context, notificacoes chan<- int64, reconectou chan<- struct{}) error {
	conn, err := pgx.Connect(ctx, e.dsn)
	if err != nil {
		return fmt.Errorf("conectar: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	if _, err := conn.Exec(ctx, "LISTEN "+canalNotify); err != nil {
		return fmt.Errorf("LISTEN %s: %w", canalNotify, err)
	}
	slog.Info("eventos: escutando", "canal", canalNotify)

	// avisa ANTES do primeiro WaitForNotification: o que entrou no banco
	// enquanto esta conexao nao existia so chega pelo catch-up.
	select {
	case reconectou <- struct{}{}:
	default:
	}

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("aguardar notificacao: %w", err)
		}
		id, err := strconv.ParseInt(n.Payload, 10, 64)
		if err != nil {
			slog.Error("eventos: notificacao com payload invalido", "payload", n.Payload)
			continue
		}
		select {
		case notificacoes <- id:
		case <-ctx.Done():
			return nil
		}
	}
}

// entregarPorID e o caminho rapido. Nao move ultimoID de proposito: o id
// sai da sequence ANTES do commit, entao a linha 100 pode confirmar
// depois da 101. Se o caminho rapido empurrasse o cursor para 101 ao
// receber a notificacao da 101, a 100 ficaria atras do cursor e o
// catch-up nunca mais a veria.
func (e *Escutador) entregarPorID(ctx context.Context, id int64) {
	if id <= e.ultimoID {
		return
	}
	if _, ja := e.entregues[id]; ja {
		return
	}
	linha, err := e.leitor.BuscarEventoPorID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		// notificacao de uma linha que a limpeza ja removeu: so acontece
		// com evento de mais de um dia, e nesse caso nao ha tela esperando.
		return
	}
	if err != nil {
		// nao e perda: a varredura pega a mesma linha, porque o cursor nao
		// avancou.
		slog.Error("eventos: buscar evento notificado", "evento_id", id, "erro", err)
		return
	}
	e.publicar(linha)
}

// varrer e o catch-up e a reconciliacao (secao 7.2, itens 2 e 3).
//
// O cursor avanca so ate a ultima linha mais velha que MargemCommit. Uma
// linha recente pode ter id menor que outra ja vista e ainda nao ter
// confirmado; passar o cursor por cima dela a perderia para sempre. A
// margem e o tempo maximo que se admite entre o INSERT do evento e o
// commit da transacao -- nas transacoes deste gateway isso e
// milissegundos, e a margem e folga pura.
func (e *Escutador) varrer(ctx context.Context) {
	linhas, err := e.leitor.ListarEventosAPartirDe(ctx, e.ultimoID)
	if err != nil {
		slog.Error("eventos: varredura", "desde_id", e.ultimoID, "erro", err)
		return
	}

	corte := time.Now().Add(-e.cfg.MargemCommit)
	novoCursor := e.ultimoID
	for _, linha := range linhas {
		if _, ja := e.entregues[linha.ID]; !ja {
			e.publicar(linha)
		}
		if linha.CriadoEm.Valid && linha.CriadoEm.Time.Before(corte) {
			novoCursor = linha.ID
		}
	}

	e.ultimoID = novoCursor
	for id := range e.entregues {
		if id <= e.ultimoID {
			delete(e.entregues, id)
		}
	}
}

func (e *Escutador) limpar(ctx context.Context) {
	idade := pgtype.Interval{Microseconds: e.cfg.Retencao.Microseconds(), Valid: true}
	removidos, err := e.leitor.LimparEventosAntigos(ctx, idade)
	if err != nil {
		slog.Error("eventos: limpar eventos antigos", "erro", err)
		return
	}
	if removidos > 0 {
		slog.Info("eventos: limpeza", "removidos", removidos)
	}
}

func (e *Escutador) publicar(linha store.Evento) {
	var evento sse.Evento
	if err := json.Unmarshal(linha.Payload, &evento); err != nil {
		slog.Error("eventos: payload ilegivel", "evento_id", linha.ID, "erro", err)
		// marca como entregue mesmo assim: reprocessar nao conserta o json,
		// e sem isso a varredura logaria o mesmo erro para sempre.
		e.entregues[linha.ID] = struct{}{}
		return
	}
	if linha.Aplicacao != nil {
		e.hub.PublicarNaAplicacao(*linha.Aplicacao, evento)
	} else {
		e.hub.Publicar(linha.Chaves, evento)
	}
	e.entregues[linha.ID] = struct{}{}
}
