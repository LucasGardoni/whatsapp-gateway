-- Barramento, fase 5. Como em canal.sql, TODA query aqui e escopada por
-- aplicacao_id dentro do proprio SQL -- o escopo e o WHERE, nunca uma
-- checagem no Go que um handler novo possa esquecer.

-- name: CriarMensagemInterna :one
-- O canal vem por (aplicacao, canal_externo) numa subconsulta escopada, e
-- nao como id: assim um id de canal de outra aplicacao, adivinhado ou
-- vazado, nao consegue gravar mensagem aqui. Zero linhas significa canal
-- inexistente PARA ESTA APLICACAO -- o handler responde 404, igual a
-- canal que nao existe, para nao confirmar a existencia do canal alheio.
--
-- aplicacao_id sai da propria linha de canal, e nao de um parametro: sao
-- necessariamente a mesma, e derivar evita a divergencia silenciosa de um
-- dia alguem passar as duas coisas e errar uma.
INSERT INTO mensagem_interna (
    canal_ref_id, aplicacao_id, remetente_externo,
    conteudo_cifrado, cifra_alg, cifra_versao
)
SELECT
    c.id, c.aplicacao_id, sqlc.arg(remetente_externo),
    sqlc.arg(conteudo_cifrado), sqlc.arg(cifra_alg), sqlc.arg(cifra_versao)
FROM canal c
WHERE c.aplicacao_id = sqlc.arg(aplicacao_id)
  AND c.canal_externo = sqlc.arg(canal_externo)
RETURNING id, canal_ref_id, criado_em;

-- name: AtualizarHashMensagemInterna :exec
UPDATE mensagem_interna SET hash_anterior = $2, hash = $3 WHERE id = $1;

-- name: ListarMensagensDoCanal :many
-- Historico paginado (secao 6.3). Devolve o ciphertext como esta gravado
-- -- byte a byte o que a aplicacao mandou. Nao ha, e nao pode haver,
-- nenhuma transformacao do conteudo neste caminho.
--
-- Escopo por aplicacao e o unico controle de acesso que existe aqui: quem
-- decide se AQUELE usuario pode ler o canal e a aplicacao, antes de
-- chamar (secao 1). O gateway nao consulta canal_assinante para decidir
-- leitura -- canal_assinante e lista de ENTREGA, nao de permissao, e
-- usa-la para autorizar seria inventar um modelo de permissao aqui.
--
-- Ordem crescente por id: e assim que a aplicacao avanca o desde_id dela
-- e retoma de onde parou depois de uma queda do SSE.
SELECT mi.id, mi.remetente_externo, mi.conteudo_cifrado,
       mi.cifra_alg, mi.cifra_versao, mi.criado_em
FROM mensagem_interna mi
JOIN canal c ON c.id = mi.canal_ref_id
WHERE c.aplicacao_id = sqlc.arg(aplicacao_id)
  AND c.canal_externo = sqlc.arg(canal_externo)
  AND mi.id > sqlc.arg(desde_id)
ORDER BY mi.id
LIMIT sqlc.arg(limite);
