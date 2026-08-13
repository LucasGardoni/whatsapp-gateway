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

-- name: BuscarLeadsNaoEngajadosParaReenvio :many
-- candidatos ao job de reenvio (fase 11): lead que recebeu disparo ou
-- clicou mas nunca chegou a 'engajado'. DISTINCT ON com ORDER BY
-- enviado_em DESC pega o disparo mais recente de cada lead -- e ele que
-- decide se a janela ja estourou. total_disparos alimenta o teto de
-- reenvio (evitar reenviar pra sempre pra quem nunca responde), aplicado
-- em Go, nao aqui -- mais facil de testar com fila falsa.
--
-- A janela passou a ser filtrada aqui, no relogio do banco (P1-08). Tem de
-- ser DEPOIS do DISTINCT ON, num CTE: filtrar dentro do WHERE mudaria a
-- semantica -- um lead com disparo recente casaria pelo disparo ANTIGO
-- dele e seria reenviado cedo demais, incomodando cliente que acabou de
-- receber mensagem. O corte vale sobre o ultimo disparo, so.
WITH ultimo_disparo AS (
    SELECT DISTINCT ON (l.id)
        l.id AS lead_id,
        l.telefone_e164,
        d.template,
        d.nome_empreendimento,
        d.enviado_em AS ultimo_disparo_em,
        (SELECT count(*) FROM disparo WHERE lead_id = l.id) AS total_disparos
    FROM lead l
    JOIN disparo d ON d.lead_id = l.id
    WHERE l.estado IN ('disparado', 'clicou')
      AND l.telefone_e164 IS NOT NULL
    ORDER BY l.id, d.enviado_em DESC
)
SELECT lead_id, telefone_e164, template, nome_empreendimento, ultimo_disparo_em, total_disparos
FROM ultimo_disparo
WHERE ultimo_disparo_em <= LOCALTIMESTAMP - make_interval(secs => sqlc.arg(janela_segundos)::double precision);
