-- +goose Up

-- Barramento, fase 1 (docs/PLANO_BARRAMENTO_MENSAGENS.md secao 5.1).
--
-- Substitui o GATEWAY_SERVICE_TOKEN unico: cada aplicacao que fala com o
-- gateway (crm, portal, rd_lider, ...) tem segredo proprio, revogavel
-- individualmente por ativo=false sem derrubar as outras.
--
-- token_hash e sha256 simples, nao bcrypt, de proposito: e segredo de alta
-- entropia gerado por maquina, nao senha de humano -- bcrypt aqui so
-- somaria latencia por requisicao. A comparacao no Go e em tempo
-- constante (crypto/subtle), como o middleware antigo ja fazia.
CREATE TABLE aplicacao (
    id         bigserial PRIMARY KEY,
    codigo     text NOT NULL UNIQUE,
    nome       text NOT NULL,
    token_hash text NOT NULL UNIQUE,
    ativo      boolean NOT NULL DEFAULT true,
    criado_em  timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX aplicacao_token_hash_idx ON aplicacao (token_hash) WHERE ativo;

-- Procedencia (secao 5.3). Nulavel aqui porque existe historico; vira NOT
-- NULL na fase 8, depois que o CRM parar de escrever pelo caminho legado.
--
-- A origem NUNCA vem no corpo da requisicao -- e derivada do token
-- autenticado. Procedencia que o chamador pode declarar nao prova nada, e
-- em base com cadeia de hash isso e pior que campo ausente, porque parece
-- confiavel.
ALTER TABLE mensagem         ADD COLUMN aplicacao_id bigint REFERENCES aplicacao (id);
ALTER TABLE mensagem_interna ADD COLUMN aplicacao_id bigint REFERENCES aplicacao (id);

CREATE INDEX mensagem_aplicacao_id_idx         ON mensagem (aplicacao_id);
CREATE INDEX mensagem_interna_aplicacao_id_idx ON mensagem_interna (aplicacao_id);

-- Semente do consumidor atual. O token_hash entra como sentinela: nao tem
-- 64 caracteres hex, entao nao casa com nenhum sha256 e a linha nasce
-- inutil para autenticar. Quem preenche o valor real e a subida do
-- gateway, a partir de GATEWAY_SERVICE_TOKEN (cmd/gateway/main.go) --
-- migration nao le variavel de ambiente, e colocar o segredo aqui o
-- deixaria versionado em git para sempre.
INSERT INTO aplicacao (codigo, nome, token_hash)
VALUES ('crm', 'CRM de corretores', 'pendente-sincronizacao-na-subida')
ON CONFLICT (codigo) DO NOTHING;

-- Backfill: tudo que existe hoje veio do CRM -- e o unico consumidor.
UPDATE mensagem
   SET aplicacao_id = (SELECT id FROM aplicacao WHERE codigo = 'crm')
 WHERE aplicacao_id IS NULL;

UPDATE mensagem_interna
   SET aplicacao_id = (SELECT id FROM aplicacao WHERE codigo = 'crm')
 WHERE aplicacao_id IS NULL;

-- +goose Down
DROP INDEX mensagem_interna_aplicacao_id_idx;
DROP INDEX mensagem_aplicacao_id_idx;
ALTER TABLE mensagem_interna DROP COLUMN aplicacao_id;
ALTER TABLE mensagem         DROP COLUMN aplicacao_id;
DROP TABLE aplicacao;
