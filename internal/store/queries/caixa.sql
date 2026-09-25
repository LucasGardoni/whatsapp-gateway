-- G1 (docs/PLANO_MULTICAIXA_E_CONVERSAS.md): uma caixa por numero.

-- name: ListarCaixasAtivas :many
SELECT * FROM caixa WHERE ativo ORDER BY id;

-- name: SincronizarCaixaSemente :exec
-- A caixa semente tem o .env como origem: a subida grava nela o que estiver
-- preenchido e nao apaga o que estiver vazio -- .env sem credencial (dev
-- sem z-api) nao pode zerar a caixa cadastrada no banco.
INSERT INTO caixa (codigo, nome, provedor, instancia_id, instancia_token, client_token, webhook_segredo)
VALUES (
    sqlc.arg(codigo),
    sqlc.arg(codigo),
    'zapi',
    NULLIF(sqlc.arg(instancia_id)::text, ''),
    NULLIF(sqlc.arg(instancia_token)::text, ''),
    NULLIF(sqlc.arg(client_token)::text, ''),
    COALESCE(NULLIF(sqlc.arg(webhook_segredo)::text, ''), replace(gen_random_uuid()::text, '-', ''))
)
ON CONFLICT (codigo) DO UPDATE
   SET instancia_id    = COALESCE(NULLIF(sqlc.arg(instancia_id)::text, ''), caixa.instancia_id),
       instancia_token = COALESCE(NULLIF(sqlc.arg(instancia_token)::text, ''), caixa.instancia_token),
       client_token    = COALESCE(NULLIF(sqlc.arg(client_token)::text, ''), caixa.client_token),
       webhook_segredo = COALESCE(NULLIF(sqlc.arg(webhook_segredo)::text, ''), caixa.webhook_segredo);

-- name: CodigoDaCaixa :one
SELECT codigo FROM caixa WHERE id = sqlc.arg(id);
