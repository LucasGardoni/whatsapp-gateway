-- +goose Up
-- chat interno usa tabelas separadas de mensagem de whatsapp
-- compartilha apenas o sse dlp nao roda em mensagem interna
CREATE TABLE canal_interno (
    id      bigserial PRIMARY KEY,
    nome    text NOT NULL,
    tipo    text NOT NULL CHECK (tipo IN ('setor', 'dm', 'thread_lead')),
    lead_id bigint REFERENCES lead (id)
);

CREATE INDEX canal_interno_lead_id_idx ON canal_interno (lead_id);

CREATE TABLE mensagem_interna (
    id         bigserial PRIMARY KEY,
    canal_id   bigint NOT NULL REFERENCES canal_interno (id),
    usuario_id bigint NOT NULL REFERENCES usuario (id),
    texto      text NOT NULL,
    criado_em  timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX mensagem_interna_canal_id_idx ON mensagem_interna (canal_id);

CREATE TABLE faq (
    id                 bigserial PRIMARY KEY,
    pergunta           text NOT NULL,
    resposta           text NOT NULL,
    origem_mensagem_id bigint REFERENCES mensagem_interna (id)
);

-- +goose Down
DROP TABLE faq;
DROP TABLE mensagem_interna;
DROP TABLE canal_interno;
