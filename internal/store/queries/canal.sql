-- Barramento, fase 4. TODA query aqui e escopada por aplicacao_id -- uma
-- aplicacao nunca le nem escreve canal de outra.
--
-- O escopo esta no WHERE de cada query, e nao numa checagem no Go, de
-- proposito: uma checagem esquecida num handler novo vaza; um WHERE que
-- nao existe faz a query devolver zero linhas, que e o lado seguro do
-- erro.

-- name: UpsertCanal :one
-- Idempotente: PUT repetido confirma em vez de duplicar. O DO UPDATE toca
-- a propria coluna para que o RETURNING traga a linha tambem quando ela ja
-- existia -- com DO NOTHING o insert conflitante nao retorna nada, e o
-- handler teria de fazer um SELECT extra so para descobrir o id.
INSERT INTO canal (aplicacao_id, canal_externo)
VALUES ($1, $2)
ON CONFLICT (aplicacao_id, canal_externo)
    DO UPDATE SET canal_externo = EXCLUDED.canal_externo
RETURNING id, aplicacao_id, canal_externo, criado_em;

-- name: BuscarCanal :one
SELECT id, aplicacao_id, canal_externo, criado_em
FROM canal
WHERE aplicacao_id = $1
  AND canal_externo = $2;

-- name: UpsertAssinante :exec
-- Idempotente. O canal_id vem de uma subconsulta escopada em vez de
-- parametro direto: assim nem um id de canal de outra aplicacao,
-- adivinhado ou vazado, consegue inserir assinante aqui.
INSERT INTO canal_assinante (canal_id, destino_externo)
SELECT c.id, sqlc.arg(destino_externo)
FROM canal c
WHERE c.aplicacao_id = sqlc.arg(aplicacao_id)
  AND c.canal_externo = sqlc.arg(canal_externo)
ON CONFLICT (canal_id, destino_externo) DO NOTHING;

-- name: RemoverAssinante :execrows
-- Devolve o numero de linhas para o handler distinguir "removido agora" de
-- "nao estava la" -- os dois respondem 204, mas so o primeiro merece log.
DELETE FROM canal_assinante ca
USING canal c
WHERE ca.canal_id = c.id
  AND c.aplicacao_id = sqlc.arg(aplicacao_id)
  AND c.canal_externo = sqlc.arg(canal_externo)
  AND ca.destino_externo = sqlc.arg(destino_externo);

-- name: ListarAssinantes :many
SELECT ca.destino_externo, ca.criado_em
FROM canal_assinante ca
JOIN canal c ON c.id = ca.canal_id
WHERE c.aplicacao_id = sqlc.arg(aplicacao_id)
  AND c.canal_externo = sqlc.arg(canal_externo)
ORDER BY ca.destino_externo;

-- name: ListarChavesDeEntregaDoCanal :many
-- Chaves do hub ("aplicacao:destino") para um canal, por ID.
--
-- Monta a chave no SQL porque quem publica (fase 5 em diante) so tem o id
-- do canal e nao deve precisar consultar a aplicacao de novo so para
-- prefixar. A composicao tem que casar exatamente com sse.ChaveDestino --
-- se divergir, o evento e publicado numa chave que ninguem assina e a
-- entrega some em silencio.
SELECT a.codigo || ':' || ca.destino_externo AS chave
FROM canal_assinante ca
JOIN canal c     ON c.id = ca.canal_id
JOIN aplicacao a ON a.id = c.aplicacao_id
WHERE ca.canal_id = $1;
