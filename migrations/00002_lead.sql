-- +goose Up
CREATE TABLE lead (
    id                 bigserial PRIMARY KEY,
    nome               text,
    telefone_e164      text,
    chat_lid           text,
    origem             text,
    empreendimento_id  bigint,
    lote_id            bigint,
    estado             text NOT NULL DEFAULT 'novo'
                       CHECK (estado IN ('novo', 'disparado', 'clicou', 'engajado',
                                          'em_atendimento', 'ganho', 'perdido')),
    corretor_id        bigint REFERENCES usuario (id),
    criado_em          timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

-- chat_lid e a identidade primaria do contato ver secao 4.3
-- telefone_e164 pode ser nulo se so houver lid
CREATE UNIQUE INDEX lead_chat_lid_idx ON lead (chat_lid) WHERE chat_lid IS NOT NULL;
CREATE INDEX lead_telefone_e164_idx ON lead (telefone_e164) WHERE telefone_e164 IS NOT NULL;
CREATE INDEX lead_estado_idx ON lead (estado);
CREATE INDEX lead_corretor_id_idx ON lead (corretor_id);

CREATE TABLE lead_payload_bruto (
    id          bigserial PRIMARY KEY,
    lead_id     bigint REFERENCES lead (id),
    origem      text NOT NULL,
    payload     jsonb NOT NULL,
    recebido_em timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX lead_payload_bruto_lead_id_idx ON lead_payload_bruto (lead_id);

CREATE TABLE disparo (
    id          bigserial PRIMARY KEY,
    lead_id     bigint NOT NULL REFERENCES lead (id),
    template    text NOT NULL,
    token       text NOT NULL UNIQUE,
    enviado_em  timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    status      text NOT NULL DEFAULT 'pendente'
);

CREATE INDEX disparo_lead_id_idx ON disparo (lead_id);

CREATE TABLE clique (
    id          bigserial PRIMARY KEY,
    token       text NOT NULL,
    lead_id     bigint REFERENCES lead (id),
    ip          text,
    user_agent  text,
    clicado_em  timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX clique_token_idx ON clique (token);
CREATE INDEX clique_lead_id_idx ON clique (lead_id);

-- +goose Down
DROP TABLE clique;
DROP TABLE disparo;
DROP TABLE lead_payload_bruto;
DROP TABLE lead;
