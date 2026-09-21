-- +goose Up

-- Barramento, fase 8 -- correcao da 00021.
--
-- A 00021 pos `mensagem.aplicacao_id` como NOT NULL, seguindo ao pe da
-- letra o item do plano ("aplicacao_id vira NOT NULL nas duas tabelas").
-- Estava errado, e o teste de integracao do webhook pegou na hora:
--
--     null value in column "aplicacao_id" of relation "mensagem"
--
-- Mensagem de ENTRADA nao tem aplicacao. Ela chega da Z-API pelo webhook:
-- quem a produziu foi o lead, do lado de fora, e nao um consumidor do
-- barramento. Exigir procedencia dela obrigaria a inventar uma aplicacao
-- para o que veio de fora -- que e pior que registrar a ausencia.
--
-- A invariante de verdade e mais estreita: **toda mensagem de SAIDA tem
-- procedencia**, porque saida so existe se alguma aplicacao pediu. E
-- isso que o CHECK abaixo exige, e ele e mais forte que o NOT NULL era
-- util: nao ha caminho de escrita de saida sem aplicacao identificada
-- desde que o GATEWAY_SERVICE_TOKEN saiu.
--
-- `mensagem_interna.aplicacao_id` continua NOT NULL, e ali esta certo:
-- ela so nasce por POST /v1/mensagens, sempre com aplicacao autenticada.

ALTER TABLE mensagem ALTER COLUMN aplicacao_id DROP NOT NULL;

ALTER TABLE mensagem ADD CONSTRAINT mensagem_saida_tem_procedencia
    CHECK (direcao <> 'saida' OR aplicacao_id IS NOT NULL);

-- +goose Down

ALTER TABLE mensagem DROP CONSTRAINT mensagem_saida_tem_procedencia;
ALTER TABLE mensagem ALTER COLUMN aplicacao_id SET NOT NULL;
