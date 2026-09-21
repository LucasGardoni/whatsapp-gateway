-- +goose Up

-- Barramento, fase 5 (docs/PLANO_BARRAMENTO_MENSAGENS.md, secao 5.4).
--
-- O gateway passa a ESCREVER mensagem interna. Ate aqui ele so lia a
-- tabela por polling (internal/chatinterno), porque quem escrevia era o
-- CRM direto no banco -- e por isso a tabela tem a forma do CRM: aponta
-- para canal_interno, tem usuario_id e guarda texto em claro.
--
-- As colunas novas sao a forma do BARRAMENTO: canal opaco, remetente
-- opaco, conteudo cifrado que o gateway nao consegue ler, e elo de
-- auditoria. As duas formas convivem ate a fase 8, quando o CRM parar de
-- escrever pelo caminho legado e as colunas antigas sairem.
ALTER TABLE mensagem_interna
    ADD COLUMN canal_ref_id      bigint REFERENCES canal (id),
    ADD COLUMN remetente_externo text,
    ADD COLUMN conteudo_cifrado  bytea,
    ADD COLUMN cifra_alg         text,
    ADD COLUMN cifra_versao      int,
    ADD COLUMN hash_anterior     text,
    ADD COLUMN hash              text;

-- usuario_id deixar de ser obrigatorio E a resolucao do problema de
-- identidade (secao 5.4): o autor passa a ser remetente_externo, string
-- opaca cujo dono e a aplicacao. O gateway nao resolve destino para
-- pessoa e nao faz join com `usuario` -- se voltar a fazer, o modelo foi
-- quebrado (secao 1).
ALTER TABLE mensagem_interna ALTER COLUMN usuario_id DROP NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN texto      DROP NOT NULL;

-- canal_id tambem sai do NOT NULL, e isso NAO esta na secao 5.4 do plano
-- -- esta aqui porque sem isso a fase 5 nao roda: canal_id aponta para
-- canal_interno, tabela do CRM, e uma mensagem escrita por uma aplicacao
-- qualquer nao tem linha la nem deve ter. A alternativa seria o gateway
-- criar canal_interno por baixo, ou seja, passar a manter a estrutura do
-- CRM -- exatamente o acoplamento que este plano existe para desfazer.
ALTER TABLE mensagem_interna ALTER COLUMN canal_id DROP NOT NULL;

-- Com os dois nulaveis, nada no schema impediria uma linha sem canal
-- nenhum -- mensagem gravada, invisivel a qualquer leitura, e o tipo de
-- falha que so aparece quando alguem reclama que a mensagem "sumiu".
-- Exatamente um dos dois, sempre: canal_id e a forma legada, canal_ref_id
-- e a do barramento. A migration da fase 8 troca esta CHECK por NOT NULL
-- em canal_ref_id.
ALTER TABLE mensagem_interna
    ADD CONSTRAINT mensagem_interna_um_canal_apenas
    CHECK (num_nonnulls(canal_id, canal_ref_id) = 1);

-- Paginacao do historico (6.3) e por (canal, id > desde_id): sem este
-- indice a leitura de um canal varre a tabela inteira, e ela cresce com
-- todo o trafego interno da empresa.
CREATE INDEX mensagem_interna_canal_ref_id_idx ON mensagem_interna (canal_ref_id, id);

-- Cadeia de auditoria PROPRIA, separada da de `mensagem`.
--
-- A tentacao e usar a mesma: uma trilha so para a empresa toda. Nao da
-- para verificar. A verificacao caminha os elos NA ORDEM em que foram
-- criados, e essa ordem, dentro de uma tabela, e o id; entre duas tabelas
-- com sequences independentes, nao existe -- so `criado_em`, que empata
-- no mesmo instante e nao desempata. Uma cadeia unica ficaria com elos
-- que ninguem consegue reordenar, ou seja, uma cadeia que nao prova nada.
--
-- Duas cadeias, cada uma verificavel por id na sua tabela, provam o mesmo
-- que a unica provaria: que nada foi inserido, removido ou alterado
-- naquela tabela.
INSERT INTO parametro (chave, valor, descricao)
VALUES (
    'auditoria_ultimo_hash_interna',
    NULL,
    'ultimo hash da cadeia de auditoria de mensagem_interna (barramento fase 5); cadeia separada da de mensagem -- ver migration 00019'
)
ON CONFLICT (chave) DO NOTHING;

-- +goose Down
DELETE FROM parametro WHERE chave = 'auditoria_ultimo_hash_interna';
DROP INDEX mensagem_interna_canal_ref_id_idx;
ALTER TABLE mensagem_interna DROP CONSTRAINT mensagem_interna_um_canal_apenas;
-- o DROP das colunas tem de vir antes de voltar os NOT NULL: e a
-- existencia de linhas do barramento (canal_id nulo) que impede o
-- ALTER ... SET NOT NULL, e elas so somem junto com as colunas novas.
DELETE FROM mensagem_interna WHERE canal_ref_id IS NOT NULL;
ALTER TABLE mensagem_interna
    DROP COLUMN canal_ref_id,
    DROP COLUMN remetente_externo,
    DROP COLUMN conteudo_cifrado,
    DROP COLUMN cifra_alg,
    DROP COLUMN cifra_versao,
    DROP COLUMN hash_anterior,
    DROP COLUMN hash;
ALTER TABLE mensagem_interna ALTER COLUMN canal_id   SET NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN texto      SET NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN usuario_id SET NOT NULL;
