-- +goose Up
CREATE TABLE usuario (
    id            bigserial PRIMARY KEY,
    nome          text NOT NULL,
    email         text NOT NULL UNIQUE,
    senha_hash    text NOT NULL,
    perfil        text NOT NULL CHECK (perfil IN ('corretor', 'supervisor', 'interno', 'admin')),
    ativo         boolean NOT NULL DEFAULT true,
    criado_em     timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE TABLE equipe (
    id    bigserial PRIMARY KEY,
    nome  text NOT NULL
);

CREATE TABLE usuario_equipe (
    usuario_id  bigint NOT NULL REFERENCES usuario (id),
    equipe_id   bigint NOT NULL REFERENCES equipe (id),
    PRIMARY KEY (usuario_id, equipe_id)
);

CREATE TABLE presenca (
    usuario_id           bigint PRIMARY KEY REFERENCES usuario (id),
    disponivel           boolean NOT NULL DEFAULT false,
    ultima_atividade_em  timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

-- +goose Down
DROP TABLE presenca;
DROP TABLE usuario_equipe;
DROP TABLE equipe;
DROP TABLE usuario;
