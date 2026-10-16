-- Responder citando, editar e excluir (migration 00030). Edicao e exclusao
-- nunca dao UPDATE em mensagem.texto: viram linha em mensagem_alteracao,
-- com elo na mesma cadeia de hash das mensagens.

-- name: BuscarMensagemParaAlterar :one
-- FOR UPDATE OF m serializa duas abas editando/excluindo a mesma mensagem.
-- idade_segundos sai do relogio do banco, o mesmo que gravou criado_em
-- (licao do P1-08: comparar com o relogio do Go desloca a janela).
SELECT m.id, m.conversa_id, m.direcao, m.tipo, m.texto, m.status, m.provedor_msg_id,
       m.autor, m.excluida_em,
       EXTRACT(EPOCH FROM (LOCALTIMESTAMP - m.criado_em))::float8 AS idade_segundos,
       c.caixa_id, c.corretor_id,
       l.chat_lid, l.telefone_e164, l.grupo_id
  FROM mensagem m
  JOIN conversa c ON c.id = m.conversa_id
  JOIN lead l ON l.id = c.lead_id
 WHERE m.id = sqlc.arg(mensagem_id)
   AND m.conversa_id = sqlc.arg(conversa_id)
   FOR UPDATE OF m;

-- name: BuscarMensagemPorProvedorMsgIDNaCaixa :one
-- webhook de edicao/revogacao/citacao: a mensagem original pelo id da z-api,
-- so na caixa que recebeu o callback.
SELECT m.id, m.conversa_id, m.direcao, m.excluida_em, c.corretor_id
  FROM mensagem m
  JOIN conversa c ON c.id = m.conversa_id
 WHERE m.provedor_msg_id = sqlc.arg(provedor_msg_id)
   AND c.caixa_id = sqlc.arg(caixa_id)
   FOR UPDATE OF m;

-- name: UltimoTextoEditado :one
-- dedupe da edicao vinda do webhook: a z-api reenvia o callback, e a mesma
-- edicao repetida nao e versao nova.
SELECT texto
  FROM mensagem_alteracao
 WHERE mensagem_id = sqlc.arg(mensagem_id)
   AND acao = 'edicao'
 ORDER BY id DESC
 LIMIT 1;

-- name: InserirAlteracaoMensagem :one
INSERT INTO mensagem_alteracao (mensagem_id, acao, texto, autor, provedor_msg_id, payload_bruto)
VALUES (
    sqlc.arg(mensagem_id),
    sqlc.arg(acao),
    sqlc.narg(texto),
    sqlc.arg(autor),
    sqlc.narg(provedor_msg_id),
    sqlc.narg(payload_bruto)
)
RETURNING id, criado_em;

-- name: AtualizarHashAlteracao :exec
UPDATE mensagem_alteracao SET hash_anterior = $2, hash = $3 WHERE id = $1;

-- name: MarcarMensagemEditada :exec
UPDATE mensagem SET editada_em = LOCALTIMESTAMP WHERE id = $1;

-- name: MarcarMensagemExcluida :execrows
UPDATE mensagem
   SET excluida_em = LOCALTIMESTAMP, excluida_por = sqlc.arg(excluida_por)
 WHERE id = sqlc.arg(id)
   AND excluida_em IS NULL;

-- name: DefinirRespostaDaMensagem :exec
-- citacao da mensagem do cliente, ligada depois do insert do webhook.
UPDATE mensagem SET responde_a_id = sqlc.arg(responde_a_id) WHERE id = sqlc.arg(id);

-- name: ListarAlteracoesDaMensagem :many
SELECT a.id, a.acao, a.texto, a.autor, a.criado_em
  FROM mensagem_alteracao a
  JOIN mensagem m ON m.id = a.mensagem_id
 WHERE a.mensagem_id = sqlc.arg(mensagem_id)
   AND m.conversa_id = sqlc.arg(conversa_id)
 ORDER BY a.id;

-- name: BuscarMensagemParaCitar :one
-- POST /v1/mensagens com responde_a: a citada tem de ser da mesma conversa.
SELECT id FROM mensagem
 WHERE id = sqlc.arg(id)
   AND conversa_id = sqlc.arg(conversa_id);
