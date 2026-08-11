-- name: CriarDisparo :one
INSERT INTO disparo (lead_id, template, token, nome_empreendimento, status)
VALUES ($1, $2, $3, $4, 'pendente')
RETURNING *;

-- name: BuscarDisparoPorToken :one
SELECT * FROM disparo WHERE token = $1;

-- name: AtualizarChatLidDoLead :exec
UPDATE lead SET chat_lid = $2 WHERE id = $1;

-- name: RegistrarClique :exec
INSERT INTO clique (token, lead_id, ip, user_agent)
VALUES ($1, $2, $3, $4);
