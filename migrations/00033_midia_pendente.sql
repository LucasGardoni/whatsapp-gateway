-- +goose Up

-- Midia recebida que nao baixou na chegada (servidor de arquivos da z-api lento
-- ou fora). Antes ficava indisponivel para sempre; agora entra nesta fila e o
-- recuperador tenta de novo enquanto o link temporario vale.
--
-- mensagem.midia_caminho entra no hash encadeado e continua sendo o que foi
-- gravado na chegada. O arquivo baixado depois vai em midia_recuperada (atalho
-- de leitura, fora do hash) e ganha um elo proprio em mensagem_alteracao com
-- acao 'midia' -- mesmo padrao da edicao (migration 00030).
ALTER TABLE mensagem ADD COLUMN midia_recuperada text;

ALTER TABLE mensagem_alteracao DROP CONSTRAINT mensagem_alteracao_acao_check;
ALTER TABLE mensagem_alteracao ADD CONSTRAINT mensagem_alteracao_acao_check
    CHECK (acao IN ('edicao', 'exclusao', 'midia'));

CREATE TABLE midia_pendente (
    mensagem_id       bigint PRIMARY KEY REFERENCES mensagem (id),
    url               text NOT NULL,
    tentativas        integer NOT NULL DEFAULT 0,
    proxima_tentativa timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    ultimo_erro       text,
    criado_em         timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX midia_pendente_proxima_idx ON midia_pendente (proxima_tentativa);

-- o que ja falhou e ainda esta dentro dos 30 dias do link da z-api. Midia que a
-- propria z-api marcou com downloadError nao tem o que baixar.
INSERT INTO midia_pendente (mensagem_id, url)
SELECT m.id, u.url
  FROM mensagem m
 CROSS JOIN LATERAL (
       SELECT COALESCE(m.payload_bruto -> 'image' ->> 'imageUrl',
                       m.payload_bruto -> 'audio' ->> 'audioUrl',
                       m.payload_bruto -> 'video' ->> 'videoUrl',
                       m.payload_bruto -> 'document' ->> 'documentUrl',
                       m.payload_bruto -> 'sticker' ->> 'stickerUrl') AS url
       ) u
 WHERE m.direcao = 'entrada'
   AND m.tipo IN ('imagem', 'audio', 'video', 'documento', 'figurinha')
   AND m.midia_caminho IS NULL
   AND m.criado_em > LOCALTIMESTAMP - interval '30 days'
   AND COALESCE(m.payload_bruto -> 'image' ->> 'downloadError', '') = ''
   AND u.url LIKE 'https://%';

-- +goose Down

-- elo 'midia' ja gravado faz o CHECK antigo falhar: apagar elo quebraria a
-- cadeia, entao o down para aqui de proposito.
DROP TABLE midia_pendente;
ALTER TABLE mensagem_alteracao DROP CONSTRAINT mensagem_alteracao_acao_check;
ALTER TABLE mensagem_alteracao ADD CONSTRAINT mensagem_alteracao_acao_check
    CHECK (acao IN ('edicao', 'exclusao'));
ALTER TABLE mensagem DROP COLUMN midia_recuperada;
