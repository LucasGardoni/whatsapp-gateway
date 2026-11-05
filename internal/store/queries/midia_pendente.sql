-- Fila de midia que nao baixou na chegada (migration 00033).

-- name: InserirMidiaPendente :exec
INSERT INTO midia_pendente (mensagem_id, url, ultimo_erro)
VALUES (sqlc.arg(mensagem_id), sqlc.arg(url), sqlc.narg(ultimo_erro))
ON CONFLICT (mensagem_id) DO NOTHING;

-- name: ListarMidiasPendentesVencidas :many
SELECT p.mensagem_id, p.url, p.tentativas, m.provedor_msg_id, m.conversa_id, c.corretor_id, cx.codigo AS caixa_codigo
  FROM midia_pendente p
  JOIN mensagem m ON m.id = p.mensagem_id
  JOIN conversa c ON c.id = m.conversa_id
  JOIN caixa cx ON cx.id = c.caixa_id
 WHERE p.proxima_tentativa <= LOCALTIMESTAMP
 ORDER BY p.proxima_tentativa
 LIMIT sqlc.arg(limite);

-- name: AdiarMidiaPendente :exec
UPDATE midia_pendente
   SET tentativas = tentativas + 1,
       proxima_tentativa = LOCALTIMESTAMP + make_interval(secs => sqlc.arg(espera_segundos)::float8),
       ultimo_erro = sqlc.narg(ultimo_erro)
 WHERE mensagem_id = sqlc.arg(mensagem_id);

-- name: RemoverMidiaPendente :exec
DELETE FROM midia_pendente WHERE mensagem_id = $1;

-- name: TravarMidiaPendente :one
-- a linha some quando outra instancia ja recuperou: ErrNoRows = nada a fazer.
SELECT mensagem_id FROM midia_pendente WHERE mensagem_id = $1 FOR UPDATE SKIP LOCKED;

-- name: DefinirMidiaRecuperada :exec
UPDATE mensagem SET midia_recuperada = sqlc.arg(midia_recuperada) WHERE id = sqlc.arg(id);
