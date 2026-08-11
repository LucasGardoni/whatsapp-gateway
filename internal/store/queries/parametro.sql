-- name: BuscarParametro :one
SELECT chave, valor, descricao
FROM parametro
WHERE chave = $1;

-- name: DefinirParametro :exec
INSERT INTO parametro (chave, valor, descricao)
VALUES ($1, $2, $3)
ON CONFLICT (chave) DO UPDATE SET valor = EXCLUDED.valor;
