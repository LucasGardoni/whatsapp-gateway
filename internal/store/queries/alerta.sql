-- name: ContarDestinatariosDistintosDesde :one
-- metrica do fator no 1 de banimento (secao 4.8): destinatarios distintos
-- que receberam mensagem de saida numa janela recente.
SELECT count(DISTINCT c.lead_id) FROM mensagem m
JOIN conversa c ON c.id = m.conversa_id
WHERE m.direcao = 'saida' AND m.criado_em >= $1;

-- name: BuscarAlertaRecente :one
SELECT * FROM alerta WHERE tipo = $1 AND criado_em >= $2 ORDER BY criado_em DESC LIMIT 1;

-- name: RegistrarAlerta :exec
INSERT INTO alerta (tipo, detalhe) VALUES ($1, $2);
