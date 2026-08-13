-- name: SelecionarPendentesParaEnvio :many
-- outbox: a fila e a propria tabela mensagem filtrada por status (secao 7).
-- FOR UPDATE SKIP LOCKED evita que dois workers peguem a mesma mensagem.
WITH selecionadas AS (
    SELECT id FROM mensagem
    WHERE status = 'pendente' AND direcao = 'saida' AND tentar_em <= LOCALTIMESTAMP
    ORDER BY criado_em
    FOR UPDATE SKIP LOCKED
    LIMIT $1
), atualizadas AS (
    UPDATE mensagem m
    SET status = 'enviando'
    FROM selecionadas s
    WHERE m.id = s.id
    RETURNING m.id, m.conversa_id, m.tipo, m.texto, m.midia_caminho, m.tentativas
)
-- conversa_id e corretor_id alimentam a publicacao do evento sse apos o
-- envio (fase 7) -- sem eles o worker nao sabe pra qual corretor notificar.
-- tipo/midia_caminho alimentam o envio de midia (fase 9).
SELECT a.id, a.conversa_id, a.tipo, a.texto, a.midia_caminho, a.tentativas, lead.chat_lid, lead.telefone_e164, c.corretor_id
FROM atualizadas a
JOIN conversa c ON c.id = a.conversa_id
JOIN lead ON lead.id = c.lead_id;

-- name: MarcarMensagemEnviada :exec
UPDATE mensagem
SET status = 'enviada', provedor_msg_id = $2, zaap_id = $3
WHERE id = $1;

-- name: MarcarMensagemParaRetentativa :exec
-- ultimo_erro fica visivel ao supervisor mesmo antes da falha definitiva
-- (fase 9 -- motivo de shadowban/falha consultavel).
--
-- tentar_em e calculado aqui, no relogio do banco, e nao no Go: quem le
-- essa coluna e SelecionarPendentesParaEnvio, que compara com
-- LOCALTIMESTAMP. Com o Go mandando o timestamp pronto, os dois lados da
-- comparacao vinham de relogios diferentes e o backoff de 30s virava -3h
-- (retentativa imediata) ou +3h (mensagem parada), dependendo do ambiente
-- -- P1-08 da auditoria. O Go passa a mandar so o atraso.
UPDATE mensagem
SET status = 'pendente',
    tentativas = tentativas + 1,
    tentar_em = LOCALTIMESTAMP + make_interval(secs => sqlc.arg(atraso_segundos)::double precision),
    ultimo_erro = sqlc.arg(ultimo_erro)
WHERE id = sqlc.arg(id);

-- name: MarcarMensagemFalhaDefinitiva :exec
UPDATE mensagem
SET status = 'falha', tentativas = tentativas + 1, ultimo_erro = $2
WHERE id = $1;

-- name: ResetarMensagensPresasEmEnvio :exec
-- roda no start do worker -- kill -9 durante o envio nao deve deixar
-- mensagem orfa em 'enviando' depois do restart.
UPDATE mensagem
SET status = 'pendente'
WHERE status = 'enviando';
