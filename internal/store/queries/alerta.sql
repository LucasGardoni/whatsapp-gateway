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
-- Debounce do monitor: existe alerta deste tipo, deste dono, dentro da
-- janela?
--
-- IS NOT DISTINCT FROM, e nao `=`: aplicacao_id nulo e o alerta da
-- empresa (volume de destinatarios distintos, que mede risco do numero
-- compartilhado), e `aplicacao_id = NULL` nunca casa com nada -- o
-- debounce silenciosamente pararia de funcionar para esse tipo, e o
-- sintoma seria uma linha nova de alerta por ciclo.
SELECT * FROM alerta
WHERE tipo = sqlc.arg(tipo)
  AND aplicacao_id IS NOT DISTINCT FROM sqlc.narg(aplicacao_id)::bigint
  AND criado_em >= LOCALTIMESTAMP - make_interval(secs => sqlc.arg(janela_segundos)::double precision)
ORDER BY criado_em DESC
LIMIT 1;

-- name: RegistrarAlerta :exec
INSERT INTO alerta (tipo, detalhe, aplicacao_id) VALUES ($1, $2, $3);

-- name: MedirVolumePorAplicacao :many
-- Fase 9: volume recente de cada aplicacao contra a media dela mesma.
--
-- "N x a media" e sempre a media DA PROPRIA aplicacao, nunca a media
-- entre aplicacoes. Comparar o Portal com o CRM diria so que um e maior
-- que o outro -- o que se quer detectar e a aplicacao que saiu do
-- comportamento DELA, que e o sintoma de laco em integracao nova.
--
-- Os dois canais entram na mesma contagem (UNION ALL) porque o abuso que
-- isto existe para pegar -- um laco -- nao escolhe canal, e uma aplicacao
-- que dobrasse o trafego trocando de canal passaria por duas contagens
-- separadas sem disparar nenhuma.
--
-- Uma varredura, dois numeros: na_janela sai de um FILTER sobre as mesmas
-- linhas ja lidas para na_base. Duas consultas leriam a janela recente
-- duas vezes, e com relogios que podem diferir entre elas.
--
-- Mensagem de ENTRADA fica fora: aplicacao_id e nulo nela (migration
-- 00023), e o que ela mede e o lead escrevendo, nao a aplicacao.
WITH movimento AS (
    SELECT aplicacao_id, criado_em
      FROM mensagem
     WHERE direcao = 'saida'
       AND aplicacao_id IS NOT NULL
       AND criado_em >= LOCALTIMESTAMP - make_interval(secs => sqlc.arg(base_segundos)::double precision)
    UNION ALL
    SELECT aplicacao_id, criado_em
      FROM mensagem_interna
     WHERE criado_em >= LOCALTIMESTAMP - make_interval(secs => sqlc.arg(base_segundos)::double precision)
)
SELECT a.id AS aplicacao_id,
       a.codigo,
       count(*) FILTER (
           WHERE m.criado_em >= LOCALTIMESTAMP - make_interval(secs => sqlc.arg(janela_segundos)::double precision)
       ) AS na_janela,
       count(*) AS na_base
  FROM aplicacao a
  JOIN movimento m ON m.aplicacao_id = a.id
 GROUP BY a.id, a.codigo
 ORDER BY a.codigo;
