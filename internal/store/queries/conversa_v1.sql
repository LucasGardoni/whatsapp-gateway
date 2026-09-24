-- G2/G3 do barramento (docs/PLANO_MULTICAIXA_E_CONVERSAS.md): API de
-- leitura de conversa e mensagem de WhatsApp para o portal. Devolver o
-- que o gateway mesmo gravou nao e consulta de dominio da aplicacao (ver
-- "o teste de contrato" no plano) -- por isso mora aqui e nao atras de
-- if de aplicacao nenhum.

-- name: BuscarConversaComContatoPorID :one
-- usada pelos dois endpoints (lista precisa do contato de cada linha,
-- GET .../mensagens precisa confirmar que a conversa existe antes de
-- listar). corretor_id fica de fora -- e divida legada (D2 do plano),
-- nao entra em contrato novo.
SELECT c.id, c.aberta_em, c.fechada_em,
       l.nome AS contato_nome, l.telefone_e164 AS contato_telefone, l.chat_lid AS contato_chat_lid
  FROM conversa c
  JOIN lead l ON l.id = c.lead_id
 WHERE c.id = sqlc.arg(id);

-- name: ListarConversas :many
-- "ultima_atividade" e a hora da mensagem mais recente da conversa, ou
-- aberta_em quando ainda nao ha nenhuma -- e o criterio de ordenacao (mais
-- recente primeiro) e de paginacao por cursor.
--
-- ultima_por_conversa e um LEFT JOIN comum com uma tabela derivada, e nao
-- LATERAL nem subquery escalar por coluna: o sqlc (analise estatica, sem
-- round-trip ao banco) so reconhece nulabilidade de coluna vinda de LEFT
-- JOIN quando o lado direito e uma relacao "normal" -- com LATERAL ou
-- subquery por coluna ele gera campo Go NAO-ponteiro para uma coluna que
-- pode vir NULL, e pgx quebra o Scan na primeira conversa sem mensagem
-- (conferido em teste de integracao). DISTINCT ON com a mesma
-- mensagem_conversa_criado_em_idx (migration 00011) evita o agregado
-- sobre a tabela inteira.
WITH ultima_por_conversa AS (
    SELECT DISTINCT ON (m.conversa_id)
           m.conversa_id, m.id, m.direcao, m.tipo, m.texto, m.status, m.criado_em
      FROM mensagem m
     ORDER BY m.conversa_id, m.criado_em DESC, m.id DESC
)
SELECT
    c.id,
    c.aberta_em,
    c.fechada_em,
    l.nome AS contato_nome,
    l.telefone_e164 AS contato_telefone,
    l.chat_lid AS contato_chat_lid,
    ultima.id AS ultima_mensagem_id,
    ultima.direcao AS ultima_mensagem_direcao,
    ultima.tipo AS ultima_mensagem_tipo,
    ultima.texto AS ultima_mensagem_texto,
    ultima.status AS ultima_mensagem_status,
    COALESCE(ultima.criado_em, c.aberta_em) AS ultima_atividade
FROM conversa c
JOIN lead l ON l.id = c.lead_id
LEFT JOIN ultima_por_conversa ultima ON ultima.conversa_id = c.id
WHERE
    (
        sqlc.arg(estado)::text = 'todas'
        OR (sqlc.arg(estado)::text = 'abertas' AND c.fechada_em IS NULL)
        OR (sqlc.arg(estado)::text = 'fechadas' AND c.fechada_em IS NOT NULL)
    )
    AND (
        sqlc.narg(desde)::timestamp IS NULL
        OR COALESCE(ultima.criado_em, c.aberta_em) >= sqlc.narg(desde)::timestamp
    )
    AND (
        sqlc.narg(cursor_atividade)::timestamp IS NULL
        OR (COALESCE(ultima.criado_em, c.aberta_em), c.id) < (sqlc.narg(cursor_atividade)::timestamp, sqlc.narg(cursor_id)::bigint)
    )
ORDER BY ultima_atividade DESC, c.id DESC
LIMIT sqlc.arg(limite);

-- name: ListarMensagensDaConversa :many
-- Mesmo contrato do historico do canal interno (secao G2/G3 do plano):
-- desde_id/limite, ordem crescente, ultimo_id pronto para a proxima
-- pagina. ate_id (nulavel) pagina PARA TRAS, para a rolagem infinita da
-- timeline ao abrir uma conversa antiga.
SELECT id, direcao, tipo, texto, midia_caminho, status, criado_em
  FROM mensagem
 WHERE conversa_id = sqlc.arg(conversa_id)
   AND id > sqlc.arg(desde_id)
   AND (sqlc.narg(ate_id)::bigint IS NULL OR id < sqlc.narg(ate_id)::bigint)
 ORDER BY id
 LIMIT sqlc.arg(limite);
