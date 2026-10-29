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
SELECT c.id, c.aberta_em, c.fechada_em, cx.codigo AS caixa,
       l.nome AS contato_nome, l.telefone_e164 AS contato_telefone, l.chat_lid AS contato_chat_lid,
       l.grupo_id AS contato_grupo_id
  FROM conversa c
  JOIN caixa cx ON cx.id = c.caixa_id
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
-- (conferido em teste de integracao; LEFT JOIN LATERAL direto continua
-- assim no sqlc 1.31). O LATERAL fica DENTRO da tabela derivada.
--
-- A tabela derivada so olha as conversas da caixa pedida e busca a ultima
-- mensagem de cada uma com LIMIT 1 na mensagem_conversa_criado_em_idx
-- (migration 00011). O DISTINCT ON anterior varria a tabela mensagem
-- inteira, de todas as caixas, a cada listagem (~0,5 s em 10/2026).
-- MATERIALIZED: inline, o planner errava a estimativa e cruzava as duas
-- listas em nested loop (milhoes de comparacoes); assim faz hash join.
WITH ultima_por_conversa AS MATERIALIZED (
    SELECT u.conversa_id, u.id, u.direcao, u.tipo, u.texto, u.status, u.criado_em
      FROM conversa cu
     CROSS JOIN LATERAL (
            SELECT m.conversa_id, m.id, m.direcao, m.tipo, m.texto, m.status, m.criado_em
              FROM mensagem m
             WHERE m.conversa_id = cu.id
             ORDER BY m.criado_em DESC, m.id DESC
             LIMIT 1
           ) u
     WHERE sqlc.narg(caixa_id)::bigint IS NULL OR cu.caixa_id = sqlc.narg(caixa_id)::bigint
)
SELECT
    c.id,
    cx.codigo AS caixa,
    c.aberta_em,
    c.fechada_em,
    l.nome AS contato_nome,
    l.telefone_e164 AS contato_telefone,
    l.chat_lid AS contato_chat_lid,
    l.grupo_id AS contato_grupo_id,
    ultima.id AS ultima_mensagem_id,
    ultima.direcao AS ultima_mensagem_direcao,
    ultima.tipo AS ultima_mensagem_tipo,
    ultima.texto AS ultima_mensagem_texto,
    ultima.status AS ultima_mensagem_status,
    COALESCE(ultima.criado_em, c.aberta_em) AS ultima_atividade,
    -- G6: entrada acima da ultima leitura DESTA aplicacao. Subquery escalar
    -- aqui e segura para o sqlc porque count(*) nunca e NULL, e o Postgres
    -- so a avalia para as linhas que sobram do LIMIT.
    (SELECT count(*)
       FROM mensagem mn
      WHERE mn.conversa_id = c.id
        AND mn.direcao = 'entrada'
        AND mn.id > COALESCE((SELECT cl.ultima_lida_id
                                FROM conversa_leitura cl
                               WHERE cl.conversa_id = c.id
                                 AND cl.aplicacao_id = sqlc.arg(aplicacao_id)::bigint
                                 AND cl.leitor = ''), 0)
    )::int AS nao_lidas,
    -- grupos (migration 00031): a mesma contagem para a pessoa pedida em
    -- `leitor`; sem leitor, igual a nao_lidas.
    (SELECT count(*)
       FROM mensagem mn
      WHERE mn.conversa_id = c.id
        AND mn.direcao = 'entrada'
        AND mn.id > COALESCE((SELECT cl.ultima_lida_id
                                FROM conversa_leitura cl
                               WHERE cl.conversa_id = c.id
                                 AND cl.aplicacao_id = sqlc.arg(aplicacao_id)::bigint
                                 AND cl.leitor = sqlc.arg(leitor)::text), 0)
    )::int AS nao_lidas_leitor
FROM conversa c
JOIN caixa cx ON cx.id = c.caixa_id
JOIN lead l ON l.id = c.lead_id
LEFT JOIN ultima_por_conversa ultima ON ultima.conversa_id = c.id
WHERE
    -- G1: caixa nula = todas.
    (sqlc.narg(caixa_id)::bigint IS NULL OR c.caixa_id = sqlc.narg(caixa_id)::bigint)
    AND (
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
--
-- texto_editado e o texto da ultima edicao (migration 00030); texto segue
-- sendo o original. Tabela derivada com LEFT JOIN, e nao LATERAL, pelo
-- mesmo motivo de ListarConversas: o sqlc so marca nulavel assim.
WITH ultima_edicao AS (
    SELECT DISTINCT ON (a.mensagem_id) a.mensagem_id, a.texto
      FROM mensagem_alteracao a
      JOIN mensagem me ON me.id = a.mensagem_id
     WHERE me.conversa_id = sqlc.arg(conversa_id)
       AND a.acao = 'edicao'
     ORDER BY a.mensagem_id, a.id DESC
)
SELECT m.id, m.direcao, m.tipo, m.texto, m.midia_caminho, m.status, m.criado_em,
       m.autor, m.editada_em, m.excluida_em, m.excluida_por, m.responde_a_id, m.encaminhada_de_id,
       m.participante_telefone, m.participante_lid, m.participante_nome,
       m.notificacao, m.notificacao_parametros,
       ed.texto AS texto_editado,
       ref.direcao AS responde_a_direcao,
       ref.tipo AS responde_a_tipo,
       ref.texto AS responde_a_texto
  FROM mensagem m
  LEFT JOIN ultima_edicao ed ON ed.mensagem_id = m.id
  LEFT JOIN mensagem ref ON ref.id = m.responde_a_id
 WHERE m.conversa_id = sqlc.arg(conversa_id)
   AND m.id > sqlc.arg(desde_id)
   AND (sqlc.narg(ate_id)::bigint IS NULL OR m.id < sqlc.narg(ate_id)::bigint)
 ORDER BY m.id
 LIMIT sqlc.arg(limite);

-- name: MarcarConversaLida :execrows
-- G6. ate_id nulo = ate a ultima mensagem da conversa. GREATEST porque
-- leitura nao retrocede: duas abas marcando fora de ordem nao podem fazer
-- o badge reaparecer. Conversa sem mensagem nao grava nada (0 linhas).
INSERT INTO conversa_leitura (aplicacao_id, conversa_id, leitor, ultima_lida_id)
SELECT sqlc.arg(aplicacao_id)::bigint, c.id, sqlc.arg(leitor)::text,
       COALESCE(sqlc.narg(ate_id)::bigint, (SELECT max(m.id) FROM mensagem m WHERE m.conversa_id = c.id))
  FROM conversa c
 WHERE c.id = sqlc.arg(conversa_id)::bigint
   AND COALESCE(sqlc.narg(ate_id)::bigint, (SELECT max(m.id) FROM mensagem m WHERE m.conversa_id = c.id)) IS NOT NULL
ON CONFLICT (aplicacao_id, conversa_id, leitor) DO UPDATE
   SET ultima_lida_id = GREATEST(conversa_leitura.ultima_lida_id, EXCLUDED.ultima_lida_id),
       atualizado_em  = now();

-- name: TravarIdentidadeDoContato :exec
-- G4. Serializa a abertura de conversa por telefone dentro da transacao:
-- lead.telefone_e164 nao tem indice unico, entao duas aberturas simultaneas
-- para o mesmo numero criariam dois leads e partiriam o historico.
SELECT pg_advisory_xact_lock(hashtext('abrir_conversa:' || sqlc.arg(telefone_e164)::text));

-- name: PreencherNomeDoLeadSeVazio :exec
-- G4. O nome informado por quem abre a conversa so vale para lead sem nome:
-- o nome de perfil que o WhatsApp mandou antes continua sendo o dado.
UPDATE lead SET nome = sqlc.arg(nome) WHERE id = sqlc.arg(id) AND (nome IS NULL OR nome = '');

-- name: BuscarMidiaDaMensagem :one
-- G10: o arquivo de uma mensagem, so se ela for da conversa pedida -- a
-- aplicacao autoriza a conversa e o gateway garante que a mensagem e dela.
SELECT tipo, texto, midia_caminho
  FROM mensagem
 WHERE id = sqlc.arg(mensagem_id)
   AND conversa_id = sqlc.arg(conversa_id);

-- name: ListarLeiturasDaConversa :many
-- quem da aplicacao leu ate onde, para o "visto por" do grupo.
SELECT leitor, ultima_lida_id, atualizado_em
  FROM conversa_leitura
 WHERE aplicacao_id = sqlc.arg(aplicacao_id)
   AND conversa_id = sqlc.arg(conversa_id)
   AND leitor <> ''
 ORDER BY leitor;
