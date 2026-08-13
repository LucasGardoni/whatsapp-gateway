-- name: TravarUltimoHashAuditoria :one
-- FOR UPDATE serializa concorrencia entre webhook (entrada) e /api/mensagens
-- (saida) escrevendo na cadeia ao mesmo tempo -- sem isso os dois podem ler
-- o mesmo hash_anterior e produzir dois elos concorrentes (fase 12).
SELECT valor FROM parametro WHERE chave = $1 FOR UPDATE;

-- name: AtualizarHashMensagem :exec
UPDATE mensagem SET hash_anterior = $2, hash = $3 WHERE id = $1;
