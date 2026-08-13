-- name: InserirLeadPayloadBruto :one
-- grava o payload bruto antes de qualquer parse (secao 10, diretriz 7).
-- lead_id comeca nulo -- so sabemos o lead depois do matcher.
INSERT INTO lead_payload_bruto (lead_id, origem, payload)
VALUES (NULL, $1, $2)
RETURNING id;

-- name: AtualizarLeadDoPayloadBruto :exec
UPDATE lead_payload_bruto SET lead_id = $2 WHERE id = $1;

-- name: BuscarLeadPorChatLid :one
SELECT * FROM lead WHERE chat_lid = $1;

-- name: BuscarLeadPorTelefone :one
SELECT * FROM lead WHERE telefone_e164 = $1;

-- name: BuscarCliqueRecentePorLead :one
SELECT * FROM clique
WHERE lead_id = $1 AND clicado_em >= $2
ORDER BY clicado_em DESC
LIMIT 1;

-- name: BuscarLeadPorTokenNoTexto :one
-- assume que o token de disparo e uma string suficientemente unica para
-- nao dar falso positivo por substring (ver secao 6 -- token pre-preenchido
-- no texto do disparo).
SELECT l.* FROM lead l
JOIN disparo d ON d.lead_id = l.id
WHERE d.token <> '' AND position(d.token IN sqlc.arg(texto)::text) > 0
ORDER BY d.enviado_em DESC
LIMIT 1;

-- name: CriarLead :one
INSERT INTO lead (nome, telefone_e164, chat_lid, origem, estado)
VALUES ($1, $2, $3, $4, 'novo')
RETURNING *;

-- name: BuscarConversaAbertaPorLead :one
SELECT * FROM conversa WHERE lead_id = $1 AND fechada_em IS NULL ORDER BY aberta_em DESC LIMIT 1;

-- name: CriarConversa :one
INSERT INTO conversa (lead_id) VALUES ($1) RETURNING *;

-- name: InserirMensagemEntrada :one
-- ON CONFLICT casa com o indice unico parcial mensagem_provedor_msg_id_idx
-- (migration 00003) -- webhook duplicado da z-api nao duplica mensagem.
INSERT INTO mensagem (conversa_id, direcao, tipo, texto, midia_caminho, provedor, provedor_msg_id, payload_bruto)
VALUES ($1, 'entrada', $2, $3, $4, $5, $6, $7)
ON CONFLICT (provedor_msg_id) WHERE provedor_msg_id IS NOT NULL DO NOTHING
RETURNING id;

-- name: AtualizarStatusMensagemPorProvedorMsgID :many
-- :many em vez de :execrows porque o handler precisa de conversa_id/corretor_id
-- pra publicar o evento sse (fase 7) -- um callback pode trazer varios ids.
UPDATE mensagem m
SET status = $2
FROM conversa c
WHERE m.provedor_msg_id = $1 AND c.id = m.conversa_id
RETURNING m.id, m.conversa_id, c.corretor_id, m.status;

-- name: RegistrarSaudeProvedor :exec
INSERT INTO provedor_saude (provedor, conectado, latencia_ms, ultimo_erro)
VALUES ($1, $2, $3, $4);

-- name: DefinirAtribuicaoCampanhaDoLead :exec
-- so preenche se ainda estiver vazio -- atribuicao e sobre a origem, a
-- primeira mensagem com externalAdReply e que vale (secao 4.5, fase 11).
UPDATE lead
SET ad_source_id = COALESCE(ad_source_id, $2),
    ctwa_clid = COALESCE(ctwa_clid, $3)
WHERE id = $1;
