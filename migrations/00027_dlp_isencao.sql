-- +goose Up

-- Isencao de DLP por mensagem. A aplicacao decide QUEM e isento (no Portal,
-- departamentos marcados por caixa e as mensagens automaticas) e manda
-- dlp_isento_por; o gateway so aceita de aplicacao com pode_isentar_dlp.
--
-- A mensagem isenta continua passando pelo motor: o que seria bloqueio
-- vira aviso em dlp_ocorrencia, para o supervisor seguir vendo o que saiu.
ALTER TABLE aplicacao ADD COLUMN IF NOT EXISTS pode_isentar_dlp boolean NOT NULL DEFAULT false;

-- quem pediu a isencao (ex.: "usuario:808", "sistema:PROGRAMADA"); nulo = sem isencao.
ALTER TABLE mensagem ADD COLUMN dlp_isento_por text;

-- +goose Down

ALTER TABLE mensagem DROP COLUMN dlp_isento_por;
ALTER TABLE aplicacao DROP COLUMN pode_isentar_dlp;
