-- +goose Up
CREATE TABLE dlp_ocorrencia (
    id          bigserial PRIMARY KEY,
    mensagem_id bigint NOT NULL REFERENCES mensagem (id),
    regra       text NOT NULL,
    decisao     text NOT NULL CHECK (decisao IN ('bloquear', 'avisar', 'liberar')),
    confianca   numeric,
    criado_em   timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX dlp_ocorrencia_mensagem_id_idx ON dlp_ocorrencia (mensagem_id);

CREATE TABLE sla_evento (
    id          bigserial PRIMARY KEY,
    lead_id     bigint NOT NULL REFERENCES lead (id),
    corretor_id bigint REFERENCES usuario (id),
    tipo        text NOT NULL,
    ocorrido_em timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX sla_evento_lead_id_idx ON sla_evento (lead_id);

-- +goose Down
DROP TABLE sla_evento;
DROP TABLE dlp_ocorrencia;
