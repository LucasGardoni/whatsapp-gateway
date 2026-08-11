-- +goose Up
CREATE TABLE midia_biblioteca (
    id        bigserial PRIMARY KEY,
    nome      text NOT NULL,
    caminho   text NOT NULL,
    categoria text,
    ativo     boolean NOT NULL DEFAULT true
);

CREATE TABLE provedor_saude (
    id            bigserial PRIMARY KEY,
    provedor      text NOT NULL,
    conectado     boolean NOT NULL DEFAULT false,
    verificado_em timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    latencia_ms   int,
    ultimo_erro   text
);

CREATE INDEX provedor_saude_provedor_idx ON provedor_saude (provedor);

-- numero b vigente mora aqui troca e um update
CREATE TABLE parametro (
    chave      text PRIMARY KEY,
    valor      text,
    descricao  text
);

-- +goose Down
DROP TABLE parametro;
DROP TABLE provedor_saude;
DROP TABLE midia_biblioteca;
