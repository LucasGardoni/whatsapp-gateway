-- +goose Up

-- Grupos de WhatsApp (docs do portal: crm/PLANO_CRM_GRUPOS.md).
--
-- O grupo e um lead sem telefone: grupo_id e o id da z-api ('120363...-group')
-- e nunca passa pela normalizacao de telefone. A conversa e por caixa, como a
-- de contato: o mesmo grupo com dois numeros nossos sao duas conversas.
--
-- participante_* diz quem falou dentro do grupo (so entrada). Nao entram no
-- hash encadeado: sao atalho de leitura, o payload bruto da linha e a prova.

ALTER TABLE lead ADD COLUMN grupo_id text;
CREATE UNIQUE INDEX lead_grupo_id_idx ON lead (grupo_id) WHERE grupo_id IS NOT NULL;

ALTER TABLE mensagem ADD COLUMN participante_telefone text;
ALTER TABLE mensagem ADD COLUMN participante_lid text;
ALTER TABLE mensagem ADD COLUMN participante_nome text;

-- evento do grupo (GROUP_PARTICIPANT_ADD, GROUP_CHANGE_SUBJECT...): mensagem
-- 'outro' sem texto, com o codigo da z-api e os parametros dele.
ALTER TABLE mensagem ADD COLUMN notificacao text;
ALTER TABLE mensagem ADD COLUMN notificacao_parametros text[];

-- leitura por pessoa dentro da aplicacao: no grupo cada usuario do portal
-- tem a propria nao lida. leitor '' e a leitura da aplicacao (G6), que segue
-- valendo para as conversas comuns.
ALTER TABLE conversa_leitura ADD COLUMN leitor text NOT NULL DEFAULT '';
ALTER TABLE conversa_leitura DROP CONSTRAINT conversa_leitura_pkey;
ALTER TABLE conversa_leitura ADD PRIMARY KEY (aplicacao_id, conversa_id, leitor);

-- +goose Down

DELETE FROM conversa_leitura WHERE leitor <> '';
ALTER TABLE conversa_leitura DROP CONSTRAINT conversa_leitura_pkey;
ALTER TABLE conversa_leitura ADD PRIMARY KEY (aplicacao_id, conversa_id);
ALTER TABLE conversa_leitura DROP COLUMN leitor;

ALTER TABLE mensagem DROP COLUMN notificacao_parametros;
ALTER TABLE mensagem DROP COLUMN notificacao;
ALTER TABLE mensagem DROP COLUMN participante_nome;
ALTER TABLE mensagem DROP COLUMN participante_lid;
ALTER TABLE mensagem DROP COLUMN participante_telefone;
DROP INDEX lead_grupo_id_idx;
ALTER TABLE lead DROP COLUMN grupo_id;
