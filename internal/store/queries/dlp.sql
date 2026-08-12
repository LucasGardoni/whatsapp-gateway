-- name: RegistrarOcorrenciaDLP :exec
-- registra decisao e confianca do motor de dlp (secao 6) -- avisar e
-- bloquear entram aqui para relatorio do supervisor; liberar so vai pro log.
INSERT INTO dlp_ocorrencia (mensagem_id, regra, decisao, confianca)
VALUES ($1, $2, $3, $4);

-- name: MarcarMensagemBloqueada :exec
UPDATE mensagem SET status = 'bloqueada' WHERE id = $1;
