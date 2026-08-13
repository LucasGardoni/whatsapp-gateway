-- name: BuscarUltimoIDMensagemInterna :one
-- ponto de partida do poller no start (fase 10) -- sem isso, todo o
-- historico de mensagem_interna reapareceria como "nova" a cada restart.
SELECT COALESCE(MAX(id), 0)::bigint FROM mensagem_interna;

-- name: ListarMensagensInternasAPartirDe :many
-- mensagem_interna e escrita direto pelo crm, sem passar pelo gateway
-- (secao 6) -- o poller so enxerga linha nova comparando id.
SELECT id, canal_id, usuario_id, texto, criado_em
FROM mensagem_interna
WHERE id > $1
ORDER BY id;
