-- name: ContarDestinatariosDistintosNaJanela :one
-- metrica do fator no 1 de banimento (secao 4.8): destinatarios distintos
-- que receberam mensagem de saida numa janela recente.
-- a janela e resolvida no relogio do banco (LOCALTIMESTAMP), nao no Go --
-- criado_em nasce de DEFAULT LOCALTIMESTAMP, entao os dois lados da
-- comparacao tem de vir do mesmo relogio (P1-08). Com o Go mandando o
-- corte pronto, uma diferenca de 3h transformava a janela de 60min em
-- "nada" ou em "quase 4h".
SELECT count(DISTINCT c.lead_id) FROM mensagem m
JOIN conversa c ON c.id = m.conversa_id
WHERE m.direcao = 'saida'
  AND m.criado_em >= LOCALTIMESTAMP - make_interval(secs => sqlc.arg(janela_segundos)::double precision);

-- name: BuscarAlertaRecente :one
SELECT * FROM alerta
WHERE tipo = sqlc.arg(tipo)
  AND criado_em >= LOCALTIMESTAMP - make_interval(secs => sqlc.arg(janela_segundos)::double precision)
ORDER BY criado_em DESC
LIMIT 1;

-- name: RegistrarAlerta :exec
INSERT INTO alerta (tipo, detalhe) VALUES ($1, $2);
