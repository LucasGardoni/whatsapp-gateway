-- name: BuscarAplicacaoPorTokenHash :one
-- Autenticacao de servico (fase 1 do barramento). Filtra por ativo aqui e
-- nao no Go: desativar uma aplicacao tem que bloquea-la mesmo que algum
-- caminho novo esqueca de checar o campo.
--
-- O hash e a chave de busca, entao o token em claro nunca sai do processo
-- que o recebeu -- nao ha SELECT por token, nem token em log de query.
--
-- token_hash volta no SELECT para o Go refazer a comparacao em tempo
-- constante (crypto/subtle). Parece redundante -- o WHERE ja compara --
-- mas a igualdade do Postgres depende de collation e para em curto-
-- circuito no primeiro byte diferente; refazer no Go custa nada e tira o
-- unico ponto de comparacao de segredo de fora do nosso controle.
SELECT id, codigo, nome, ativo, token_hash
FROM aplicacao
WHERE token_hash = $1
  AND ativo;

-- name: BuscarAplicacaoPorCodigo :one
SELECT id, codigo, nome, ativo, criado_em
FROM aplicacao
WHERE codigo = $1;

-- name: ListarAplicacoes :many
SELECT id, codigo, nome, ativo, criado_em
FROM aplicacao
ORDER BY codigo;
