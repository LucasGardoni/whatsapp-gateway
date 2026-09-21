-- name: BuscarUltimoIDMensagemInterna :one
-- ponto de partida do poller no start (fase 10) -- sem isso, todo o
-- historico de mensagem_interna reapareceria como "nova" a cada restart.
SELECT COALESCE(MAX(id), 0)::bigint FROM mensagem_interna;

-- name: ListarMensagensInternasAPartirDe :many
-- mensagem_interna e escrita direto pelo crm, sem passar pelo gateway
-- (secao 6) -- o poller so enxerga linha nova comparando id.
--
-- canal_ref_id IS NULL restringe o poller as linhas do CAMINHO LEGADO
-- (barramento, fase 5). A partir da fase 5 o gateway tambem escreve nesta
-- tabela, e essas linhas ja sao publicadas pelo proprio handler, para a
-- lista de destinos exata do canal. Sem este filtro o poller republicaria
-- cada uma delas em broadcast para a aplicacao inteira: evento duplicado
-- na tela de quem e do canal, e evento indevido na de quem nao e.
--
-- O filtro some junto com o poller, na fase 8.
SELECT id, canal_id, usuario_id, texto, criado_em
FROM mensagem_interna
WHERE id > $1
  AND canal_ref_id IS NULL
ORDER BY id;
