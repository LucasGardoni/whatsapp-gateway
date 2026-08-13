-- name: PurgarProvedorSaudeAntigo :execrows
-- Retencao (fase 6). O monitor grava 1 linha a cada 30s: 2.880 por dia,
-- ~1 milhao por ano, para uma tabela cuja unica consulta e "qual a leitura
-- mais recente". Historico antigo de saude nao tem consumidor.
--
-- PRESERVA as transicoes de estado (conectado != anterior): e delas que
-- sai "quando a instancia caiu", que foi exatamente a pergunta que se
-- precisou responder em 2026-08-13. Apagar tudo por idade destruiria a
-- unica informacao com valor retroativo da tabela.
DELETE FROM provedor_saude
WHERE id IN (
    SELECT id FROM (
        SELECT id,
               verificado_em,
               conectado,
               lag(conectado) OVER (PARTITION BY provedor ORDER BY id) AS anterior
        FROM provedor_saude
    ) t
    WHERE t.verificado_em < LOCALTIMESTAMP - make_interval(secs => sqlc.arg(retencao_segundos)::double precision)
      AND t.anterior IS NOT DISTINCT FROM t.conectado
);

-- name: PurgarPayloadBrutoAntigo :execrows
-- Retencao (fase 6). lead_payload_bruto guarda o payload cru de TODO
-- webhook (diretriz 7: gravar antes de parsear), incluindo o que foi
-- descartado por fromMe/grupo. Cresce com o trafego e nunca era limpo.
--
-- O payload serve para depurar recepcao e para reprocessar uma mensagem
-- mal interpretada -- as duas coisas com validade curta. O dado de negocio
-- ja esta em lead/conversa/mensagem e nao depende disto.
DELETE FROM lead_payload_bruto
WHERE recebido_em < LOCALTIMESTAMP - make_interval(secs => sqlc.arg(retencao_segundos)::double precision);

-- name: BuscarParametro :one
SELECT chave, valor, descricao
FROM parametro
WHERE chave = $1;

-- name: DefinirParametro :exec
INSERT INTO parametro (chave, valor, descricao)
VALUES ($1, $2, $3)
ON CONFLICT (chave) DO UPDATE SET valor = EXCLUDED.valor;
