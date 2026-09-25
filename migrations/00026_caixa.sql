-- +goose Up

-- G1 (docs/PLANO_MULTICAIXA_E_CONVERSAS.md): uma caixa por numero de
-- WhatsApp. Ate aqui o gateway falava com uma unica instancia, global do
-- processo, e nada no banco dizia por qual numero uma conversa passava.
--
-- Credenciais no banco, e nao no .env, porque e a unica forma de cadastrar
-- numero novo sem deploy -- a mesma logica de `aplicacao`. O .env continua
-- sendo a origem da caixa semente (ver SincronizarCaixaSemente).
CREATE TABLE caixa (
    id              bigserial PRIMARY KEY,
    codigo          text NOT NULL UNIQUE,
    nome            text NOT NULL,
    provedor        text NOT NULL CHECK (provedor IN ('zapi', 'fake')),
    instancia_id    text,
    instancia_token text,
    client_token    text,
    -- segmento de path do webhook desta caixa. Nasce aleatorio: um valor
    -- fixo aqui ficaria versionado e abriria o webhook para quem le o git.
    webhook_segredo text NOT NULL UNIQUE DEFAULT replace(gen_random_uuid()::text, '-', ''),
    ativo           boolean NOT NULL DEFAULT true,
    criado_em       timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

-- Semente: o numero que ja existe. Codigo igual ao CAIXA_CODIGO padrao e
-- ao crm_caixa.codigo do portal. Credenciais e segredo entram na subida do
-- gateway, a partir do .env -- migration nao le variavel de ambiente.
INSERT INTO caixa (codigo, nome, provedor)
VALUES ('rod_lider', 'Rodoviario Lider', 'zapi')
ON CONFLICT (codigo) DO NOTHING;

-- conversa.caixa_id, e nao mensagem.caixa_id: a mensagem herda a caixa pela
-- conversa, e uma conversa nunca muda de numero.
--
-- NOT NULL na mesma migration: o ALTER trava a tabela, entao nenhuma linha
-- nasce entre o backfill e a restricao. O que sobra e ordem de deploy --
-- binario antigo gravando conversa depois da migration falha --, e por isso
-- a ordem e parar, migrar, subir.
ALTER TABLE conversa ADD COLUMN caixa_id bigint REFERENCES caixa (id);

UPDATE conversa
   SET caixa_id = (SELECT id FROM caixa WHERE codigo = 'rod_lider')
 WHERE caixa_id IS NULL;

ALTER TABLE conversa ALTER COLUMN caixa_id SET NOT NULL;

-- o mesmo contato pode falar com dois numeros: sao duas conversas, uma por
-- caixa. A garantia do P1-13 (uma aberta por lead) passa a valer por caixa.
DROP INDEX conversa_lead_aberta_idx;
CREATE UNIQUE INDEX conversa_lead_aberta_idx
    ON conversa (lead_id, caixa_id)
    WHERE fechada_em IS NULL;

CREATE INDEX conversa_caixa_id_idx ON conversa (caixa_id);

-- saude por numero: com dois, "a instancia caiu" precisa dizer qual.
-- Nulavel porque o historico anterior nao sabe de caixa.
ALTER TABLE provedor_saude ADD COLUMN caixa_id bigint REFERENCES caixa (id);

-- +goose Down

ALTER TABLE provedor_saude DROP COLUMN caixa_id;
DROP INDEX conversa_caixa_id_idx;
DROP INDEX conversa_lead_aberta_idx;
-- falha se houver duas conversas abertas do mesmo lead em caixas diferentes:
-- fechar uma delas antes de descer.
CREATE UNIQUE INDEX conversa_lead_aberta_idx
    ON conversa (lead_id)
    WHERE fechada_em IS NULL;
ALTER TABLE conversa DROP COLUMN caixa_id;
DROP TABLE caixa;
