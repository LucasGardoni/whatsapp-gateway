-- +goose Up

-- Barramento, fase 8 -- o passo final da migracao do chat interno.
--
-- Apaga as colunas do caminho legado de `mensagem_interna`:
--
--   canal_id   -> canal_ref_id (canal opaco, migration 00021)
--   usuario_id -> remetente_externo (string opaca)
--   texto      -> conteudo_cifrado (AES-256-GCM, chave so no CRM)
--
-- Depois disto o gateway nao tem mais NENHUMA coluna de mensagem interna
-- que ele consiga ler. A §2 do plano ("o gateway nunca le conteudo de
-- mensagem interna") deixa de ser uma promessa de codigo e passa a ser
-- uma propriedade do schema.
--
-- `canal_interno` NAO e removida: ela virou metadado do CRM (nome, tipo,
-- lead ancorado, regra de acesso) e continua sendo lida por ele. O que
-- acabou foi a relacao dela com `mensagem_interna` -- o gateway nao a
-- referencia em lugar nenhum.

-- +goose StatementBegin
DO $$
DECLARE
    em_claro bigint;
BEGIN
    SELECT count(*) INTO em_claro
    FROM mensagem_interna
    WHERE texto IS NOT NULL AND conteudo_cifrado IS NULL;

    IF em_claro > 0 THEN
        -- Recusa em vez de apagar. O gateway NAO tem como cifrar estas
        -- linhas: a chave vive no CRM, que e o ponto inteiro da fase 8.
        -- Deixar passar aqui perderia o historico de forma definitiva.
        RAISE EXCEPTION
            'ha % mensagem(ns) interna(s) ainda em texto claro. Rode antes, no CRM: php spark chat-interno:migrar-legado',
            em_claro;
    END IF;
END
$$;
-- +goose StatementEnd

-- o CHECK cita canal_id; sai antes das colunas e volta olhando so o que
-- restou.
ALTER TABLE mensagem_interna DROP CONSTRAINT mensagem_interna_um_canal_apenas;

ALTER TABLE mensagem_interna DROP COLUMN canal_id;
ALTER TABLE mensagem_interna DROP COLUMN usuario_id;
ALTER TABLE mensagem_interna DROP COLUMN texto;

-- o que era "uma das duas colunas de canal" agora e simplesmente
-- obrigatorio: nao existe mais mensagem interna fora de um canal opaco.
ALTER TABLE mensagem_interna ALTER COLUMN canal_ref_id SET NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN remetente_externo SET NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN conteudo_cifrado SET NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN cifra_alg SET NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN cifra_versao SET NOT NULL;

-- +goose Down

ALTER TABLE mensagem_interna ALTER COLUMN cifra_versao DROP NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN cifra_alg DROP NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN conteudo_cifrado DROP NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN remetente_externo DROP NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN canal_ref_id DROP NOT NULL;

ALTER TABLE mensagem_interna ADD COLUMN canal_id bigint REFERENCES canal_interno(id);
ALTER TABLE mensagem_interna ADD COLUMN usuario_id bigint REFERENCES usuario(id);
ALTER TABLE mensagem_interna ADD COLUMN texto text;

-- o rollback devolve as COLUNAS, nunca o conteudo: o texto claro foi
-- apagado e so existe cifrado, com uma chave que este banco nao tem.
ALTER TABLE mensagem_interna ADD CONSTRAINT mensagem_interna_um_canal_apenas
    CHECK (num_nonnulls(canal_id, canal_ref_id) = 1);
