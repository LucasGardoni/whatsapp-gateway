package observabilidade

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Banco le o estado do Postgres para o painel. O pool vem ao vivo (Stat()
// e memoria do processo); o resto e consulta, e fica em cache: o painel
// aberto em varias telas nao pode virar carga no banco que ele vigia.
type Banco struct {
	pool *pgxpool.Pool
	ttl  time.Duration

	mu           sync.Mutex
	ultimo       RetratoBanco
	atualizadoEm time.Time
	atualizando  bool
}

func NovoBanco(pool *pgxpool.Pool) *Banco {
	return &Banco{pool: pool, ttl: 30 * time.Second}
}

// Pool e o retrato do pgxpool deste processo.
func (b *Banco) Pool() RetratoPool {
	if b == nil || b.pool == nil {
		return RetratoPool{}
	}
	s := b.pool.Stat()
	return RetratoPool{
		Max:               s.MaxConns(),
		Total:             s.TotalConns(),
		EmUso:             s.AcquiredConns(),
		Ociosas:           s.IdleConns(),
		Abrindo:           s.ConstructingConns(),
		Aquisicoes:        s.AcquireCount(),
		AquisicaoMediaMs:  mediaMs(s.AcquireDuration(), s.AcquireCount()),
		Esperas:           s.EmptyAcquireCount(),
		EsperaTotalMs:     float64(s.EmptyAcquireWaitTime().Microseconds()) / 1000,
		Canceladas:        s.CanceledAcquireCount(),
		NovasConexoes:     s.NewConnsCount(),
		FechadasPorIdade:  s.MaxLifetimeDestroyCount(),
		FechadasPorOciosa: s.MaxIdleDestroyCount(),
	}
}

func mediaMs(d time.Duration, n int64) float64 {
	if n == 0 {
		return 0
	}
	return float64(d.Microseconds()) / 1000 / float64(n)
}

// Ping confirma que o banco responde, com teto curto: e o que /health/ready usa.
func (b *Banco) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return b.pool.Ping(ctx)
}

// Retrato devolve o cache. Vencido, atualiza -- uma consulta por vez: quem
// chega durante a atualizacao leva o retrato anterior em vez de somar
// outra rodada de consultas.
func (b *Banco) Retrato(ctx context.Context) RetratoBanco {
	if b == nil || b.pool == nil {
		return RetratoBanco{Erro: "pool indisponivel"}
	}
	b.mu.Lock()
	if time.Since(b.atualizadoEm) < b.ttl || b.atualizando {
		r := b.ultimo
		b.mu.Unlock()
		return r
	}
	b.atualizando = true
	b.mu.Unlock()

	r := b.consultar(ctx)

	b.mu.Lock()
	b.ultimo = r
	b.atualizadoEm = time.Now()
	b.atualizando = false
	b.mu.Unlock()
	return r
}

// Atualizar forca a leitura (amostrador), respeitando o mesmo cache.
func (b *Banco) Atualizar(ctx context.Context) { _ = b.Retrato(ctx) }

func (b *Banco) consultar(ctx context.Context) RetratoBanco {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	inicio := time.Now()
	r := RetratoBanco{AtualizadoEm: inicio}

	if err := b.pool.QueryRow(ctx, consultaServidor).Scan(
		&r.Servidor.MaxConexoes, &r.Servidor.TamanhoBytes, &r.Servidor.Versao,
		&r.Servidor.Commits, &r.Servidor.Rollbacks, &r.Servidor.Deadlocks,
		&r.Servidor.CacheHitPct, &r.Servidor.TempBytes,
	); err != nil {
		r.Erro = "servidor: " + err.Error()
		return r
	}

	if err := b.pool.QueryRow(ctx, consultaAtividade).Scan(
		&r.Atividade.Total, &r.Atividade.DesteBanco, &r.Atividade.Ativas, &r.Atividade.Ociosas,
		&r.Atividade.OciosasEmTransacao, &r.Atividade.AguardandoLock,
		&r.Atividade.MaiorConsultaS, &r.Atividade.MaiorTransacaoS,
	); err != nil {
		r.Erro = "atividade: " + err.Error()
		return r
	}

	linhas, err := b.pool.Query(ctx, consultaFila)
	if err != nil {
		r.Erro = "fila: " + err.Error()
		return r
	}
	for linhas.Next() {
		var f RetratoFila
		if err := linhas.Scan(&f.Caixa, &f.Pendentes, &f.Enviando, &f.MaisAntigaS, &f.Falhas24h, &f.Bloqueadas24h); err != nil {
			linhas.Close()
			r.Erro = "fila: " + err.Error()
			return r
		}
		r.Fila = append(r.Fila, f)
	}
	linhas.Close()
	if err := linhas.Err(); err != nil {
		r.Erro = "fila: " + err.Error()
		return r
	}

	linhas, err = b.pool.Query(ctx, consultaTabelas)
	if err != nil {
		r.Erro = "tabelas: " + err.Error()
		return r
	}
	for linhas.Next() {
		var t RetratoTabela
		if err := linhas.Scan(&t.Nome, &t.Linhas, &t.Bytes, &t.LinhasMortas, &t.SeqScans, &t.IdxScans); err != nil {
			linhas.Close()
			r.Erro = "tabelas: " + err.Error()
			return r
		}
		r.Tabelas = append(r.Tabelas, t)
	}
	linhas.Close()

	r.ConsultaMs = float64(time.Since(inicio).Microseconds()) / 1000
	return r
}

// As consultas sao todas de catalogo ou cobertas por indice: pg_stat_* e
// memoria do servidor, e a fila filtra por (direcao, status), que tem
// mensagem_direcao_status_idx. Nada aqui varre `mensagem` inteira.
const consultaServidor = `
SELECT current_setting('max_connections')::int,
       pg_database_size(current_database()),
       current_setting('server_version'),
       d.xact_commit, d.xact_rollback, d.deadlocks,
       COALESCE(round(100.0 * d.blks_hit / NULLIF(d.blks_hit + d.blks_read, 0), 2), 100)::float8,
       d.temp_bytes
FROM pg_stat_database d
WHERE d.datname = current_database()`

// a propria conexao (pg_backend_pid) fica fora da maior consulta/transacao:
// e ela que esta rodando isto.
const consultaAtividade = `
SELECT (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'),
       count(*),
       count(*) FILTER (WHERE state = 'active' AND pid <> pg_backend_pid()),
       count(*) FILTER (WHERE state = 'idle'),
       count(*) FILTER (WHERE state LIKE 'idle in transaction%'),
       count(*) FILTER (WHERE wait_event_type = 'Lock'),
       COALESCE(max(EXTRACT(EPOCH FROM now() - query_start)) FILTER (WHERE state = 'active' AND pid <> pg_backend_pid()), 0)::float8,
       COALESCE(max(EXTRACT(EPOCH FROM now() - xact_start)) FILTER (WHERE xact_start IS NOT NULL AND pid <> pg_backend_pid()), 0)::float8
FROM pg_stat_activity
WHERE datname = current_database() AND backend_type = 'client backend'`

// idade em LOCALTIMESTAMP: mensagem.criado_em e gravado no relogio do
// banco (P1-08), entao a conta tambem e feita la.
//
// Parte de `mensagem` pelo indice (direcao, status) e so depois junta a
// caixa: partir da caixa juntaria todas as conversas dela.
const consultaFila = `
WITH f AS (
    SELECT c.caixa_id, m.status, m.criado_em
    FROM mensagem m
    JOIN conversa c ON c.id = m.conversa_id
    WHERE m.direcao = 'saida'
      AND (m.status IN ('pendente', 'enviando')
           OR (m.status IN ('falha', 'bloqueada') AND m.criado_em > LOCALTIMESTAMP - interval '24 hours'))
)
SELECT cx.codigo,
       count(*) FILTER (WHERE f.status = 'pendente'),
       count(*) FILTER (WHERE f.status = 'enviando'),
       COALESCE(EXTRACT(EPOCH FROM LOCALTIMESTAMP - min(f.criado_em) FILTER (WHERE f.status = 'pendente')), 0)::float8,
       count(*) FILTER (WHERE f.status = 'falha'),
       count(*) FILTER (WHERE f.status = 'bloqueada')
FROM caixa cx
LEFT JOIN f ON f.caixa_id = cx.id
WHERE cx.ativo
GROUP BY cx.codigo
ORDER BY cx.codigo`

const consultaTabelas = `
SELECT relname, n_live_tup, pg_total_relation_size(relid), n_dead_tup, COALESCE(seq_scan, 0), COALESCE(idx_scan, 0)
FROM pg_stat_user_tables
ORDER BY pg_total_relation_size(relid) DESC
LIMIT 10`
