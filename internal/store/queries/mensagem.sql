-- name: BuscarConversaPorID :one
SELECT * FROM conversa WHERE id = $1;

-- name: CriarMensagemSaida :one
-- mensagem criada pelo CRM via POST /api/mensagens (fase 7). Direcao e
-- provedor seguem fixos: todo atendimento humano sai pelo numero B (zapi),
-- nunca pelo numero A.
--
-- tipo e midia_caminho passaram a vir do chamador (fase 3). Antes o tipo
-- era 'texto' fixo aqui: o outbox ja sabia mandar midia desde a fase 9,
-- mas nao havia como o CRM pedir isso -- a legenda ate chegava, o arquivo
-- nao. O handler valida o tipo antes; o CHECK do schema e a rede de
-- seguranca.
--
-- aplicacao_id e a procedencia (barramento, fase 1): vem do token
-- autenticado, nunca do corpo. Nulavel enquanto o caminho legado
-- (ExigirTokenServico) existir -- vira NOT NULL na fase 8.
INSERT INTO mensagem (conversa_id, direcao, tipo, texto, midia_caminho, provedor, aplicacao_id)
VALUES (
    sqlc.arg(conversa_id),
    'saida',
    sqlc.arg(tipo),
    sqlc.narg(texto),
    sqlc.narg(midia_caminho),
    'zapi',
    sqlc.narg(aplicacao_id)
)
RETURNING *;
