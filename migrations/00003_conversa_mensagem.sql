-- +goose Up
CREATE TABLE conversa (
    id          bigserial PRIMARY KEY,
    lead_id     bigint NOT NULL REFERENCES lead (id),
    corretor_id bigint REFERENCES usuario (id),
    aberta_em   timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    fechada_em  timestamp
);

CREATE INDEX conversa_lead_id_idx ON conversa (lead_id);
CREATE INDEX conversa_corretor_id_idx ON conversa (corretor_id);

CREATE TABLE mensagem (
    id               bigserial PRIMARY KEY,
    conversa_id      bigint NOT NULL REFERENCES conversa (id),
    direcao          text NOT NULL CHECK (direcao IN ('entrada', 'saida')),
    tipo             text NOT NULL
                     CHECK (tipo IN ('texto', 'imagem', 'audio', 'video', 'documento', 'outro')),
    texto            text,
    midia_caminho    text,
    provedor         text NOT NULL,
    provedor_msg_id  text,
    zaap_id          text,
    status           text NOT NULL DEFAULT 'pendente'
                     CHECK (status IN ('pendente', 'enviando', 'enviada', 'entregue',
                                        'lida', 'falha', 'bloqueada')),
    tentativas       int NOT NULL DEFAULT 0,
    tentar_em        timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    payload_bruto    jsonb,
    criado_em        timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    hash_anterior    text,
    hash             text
);

-- idempotencia a z-api pode reenviar webhook
CREATE UNIQUE INDEX mensagem_provedor_msg_id_idx ON mensagem (provedor_msg_id)
    WHERE provedor_msg_id IS NOT NULL;

CREATE INDEX mensagem_conversa_id_idx ON mensagem (conversa_id);

-- outbox a fila e a propria tabela filtrada por status
CREATE INDEX mensagem_outbox_idx ON mensagem (tentar_em)
    WHERE status = 'pendente' AND direcao = 'saida';

-- +goose Down
DROP TABLE mensagem;
DROP TABLE conversa;
