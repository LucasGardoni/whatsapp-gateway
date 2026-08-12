-- name: BuscarConversaPorID :one
SELECT * FROM conversa WHERE id = $1;

-- name: CriarMensagemSaida :one
-- mensagem criada pelo CRM via POST /api/mensagens (fase 7). Direcao e
-- provedor sao fixos: todo atendimento humano sai pelo numero B (zapi),
-- nunca pelo numero A. So texto na v1 -- midia depende do provedor
-- suportar (fase 9).
INSERT INTO mensagem (conversa_id, direcao, tipo, texto, provedor)
VALUES ($1, 'saida', 'texto', $2, 'zapi')
RETURNING *;
