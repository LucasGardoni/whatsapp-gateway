-- Barramento, fase 7. A tabela `evento` e o barramento entre INSTANCIAS
-- do gateway -- ver migration 00020 para por que ela existe.

-- name: RegistrarEvento :exec
-- Gravado SEMPRE dentro da transacao que produziu o fato. O gatilho da
-- migration 00020 dispara o pg_notify no commit; nao ha nada a chamar
-- depois.
INSERT INTO evento (chaves, aplicacao, payload)
VALUES ($1, $2, $3);

-- name: BuscarUltimoIDEvento :one
-- Cursor inicial: uma instancia que sobe nao reproduz o historico. Quem
-- estava conectado antes dela subir perdeu a conexao junto e busca o
-- estado atual na reconexao do EventSource.
SELECT coalesce(max(id), 0)::bigint FROM evento;

-- name: BuscarEventoPorID :one
-- Caminho rapido: o NOTIFY traz o id e esta consulta traz a linha. Devolve
-- ErrNoRows se a limpeza ja passou por cima, o que so acontece com uma
-- notificacao velha de mais de um dia -- nao e erro.
SELECT id, chaves, aplicacao, payload, criado_em
FROM evento
WHERE id = $1;

-- name: ListarEventosAPartirDe :many
-- Catch-up e reconciliacao: a MESMA consulta, porque a diferenca entre
-- "perdi a conexao" e "confere se perdi alguma coisa" e so a frequencia.
-- LIMIT para uma queda longa nao virar um SELECT de milhoes de linhas num
-- unico ciclo -- o resto vem no ciclo seguinte.
--
-- criado_em volta junto porque o cursor nao pode avancar ate o maior id
-- visto: ids saem da sequence ANTES do commit, entao a linha 100 pode
-- confirmar depois da 101. Ver escutador.varrer.
SELECT id, chaves, aplicacao, payload, criado_em
FROM evento
WHERE id > $1
ORDER BY id
LIMIT 500;

-- name: LimparEventosAntigos :execrows
-- Evento e roteamento, nao historico: quem precisa do que aconteceu tem
-- `mensagem`, `mensagem_interna` e a cadeia de auditoria. Sem esta
-- limpeza a tabela cresce com TODO o trafego da empresa para sempre.
DELETE FROM evento WHERE criado_em < now() - sqlc.arg(idade)::interval;
