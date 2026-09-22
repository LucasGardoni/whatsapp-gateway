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
--
-- Os tres campos de politica (limites e pode_ler_metricas) vem no mesmo
-- SELECT de proposito: sao lidos a cada requisicao, e o cache do
-- middleware ja guarda a linha inteira -- buscar cada um na hora de usar
-- seria um SELECT por politica por requisicao.
SELECT id, codigo, nome, ativo, token_hash,
       limite_requisicoes_por_minuto, limite_conteudo_cifrado_bytes,
       pode_ler_metricas
FROM aplicacao
WHERE token_hash = $1
  AND ativo;

-- name: BuscarAplicacaoPorCodigo :one
SELECT id, codigo, nome, ativo, criado_em,
       limite_requisicoes_por_minuto, limite_conteudo_cifrado_bytes,
       pode_ler_metricas
FROM aplicacao
WHERE codigo = $1;

-- name: ListarAplicacoes :many
SELECT id, codigo, nome, ativo, criado_em,
       limite_requisicoes_por_minuto, limite_conteudo_cifrado_bytes,
       pode_ler_metricas
FROM aplicacao
ORDER BY codigo;
