-- Grupos de WhatsApp (migration 00031). O grupo e um lead com grupo_id e sem
-- telefone; a conversa segue sendo por caixa.

-- name: TravarGrupo :exec
-- serializa mensagens simultaneas do mesmo grupo: sem isso duas goroutines do
-- webhook criariam dois leads para o mesmo grupo.
SELECT pg_advisory_xact_lock(hashtext('grupo:' || sqlc.arg(grupo_id)::text));

-- name: BuscarLeadPorGrupo :one
SELECT * FROM lead WHERE grupo_id = sqlc.arg(grupo_id);

-- name: CriarLeadGrupo :one
INSERT INTO lead (nome, grupo_id, origem, estado)
VALUES (sqlc.narg(nome), sqlc.arg(grupo_id), 'grupo', 'novo')
RETURNING *;

-- name: AtualizarNomeDoGrupo :exec
-- o nome do grupo muda (GROUP_CHANGE_SUBJECT) e cada mensagem traz o atual.
UPDATE lead SET nome = sqlc.arg(nome)
 WHERE id = sqlc.arg(id) AND nome IS DISTINCT FROM sqlc.arg(nome);

-- name: BuscarUltimaConversaPorLead :one
-- grupo nao abre conversa nova a cada volta do numero: reabre a ultima, para
-- o historico continuar no mesmo lugar.
SELECT * FROM conversa
 WHERE lead_id = sqlc.arg(lead_id) AND caixa_id = sqlc.arg(caixa_id)
 ORDER BY aberta_em DESC, id DESC LIMIT 1;

-- name: ReabrirConversa :exec
UPDATE conversa SET fechada_em = NULL WHERE id = sqlc.arg(id) AND fechada_em IS NOT NULL;

-- name: FecharConversa :execrows
UPDATE conversa SET fechada_em = LOCALTIMESTAMP WHERE id = sqlc.arg(id) AND fechada_em IS NULL;

-- name: InserirMensagemEntradaGrupo :one
-- mesma deduplicacao de InserirMensagemEntrada, com quem falou no grupo.
INSERT INTO mensagem (conversa_id, direcao, tipo, texto, midia_caminho, provedor, provedor_msg_id, payload_bruto,
                      participante_telefone, participante_lid, participante_nome, notificacao, notificacao_parametros)
VALUES (sqlc.arg(conversa_id), 'entrada', sqlc.arg(tipo), sqlc.narg(texto), sqlc.narg(midia_caminho), sqlc.arg(provedor),
        sqlc.narg(provedor_msg_id), sqlc.arg(payload_bruto),
        sqlc.narg(participante_telefone), sqlc.narg(participante_lid), sqlc.narg(participante_nome),
        sqlc.narg(notificacao), sqlc.narg(notificacao_parametros)::text[])
ON CONFLICT (provedor_msg_id) WHERE provedor_msg_id IS NOT NULL DO NOTHING
RETURNING id;

-- name: ListarGruposDaCaixa :many
-- base da reconciliacao com o get-groups: todo grupo que o gateway conhece
-- nesta caixa, aberto ou fechado.
SELECT DISTINCT ON (l.id) c.id AS conversa_id, c.fechada_em, l.id AS lead_id, l.grupo_id, l.nome
  FROM conversa c
  JOIN lead l ON l.id = c.lead_id
 WHERE c.caixa_id = sqlc.arg(caixa_id)
   AND l.grupo_id IS NOT NULL
 ORDER BY l.id, c.aberta_em DESC, c.id DESC;

-- name: BuscarGrupoDaConversa :one
SELECT c.id, c.caixa_id, c.fechada_em, l.grupo_id, l.nome
  FROM conversa c
  JOIN lead l ON l.id = c.lead_id
 WHERE c.id = sqlc.arg(id) AND l.grupo_id IS NOT NULL;
