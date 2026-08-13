-- +goose Up
-- atribuicao de campanha capturada do externalAdReply da z-api quando a
-- conversa nasce de um anuncio click-to-whatsapp (secao 4.5) -- persistido
-- mesmo sem uso imediato, e so preenchido uma vez (nao sobrescreve).
ALTER TABLE lead ADD COLUMN ad_source_id text;
ALTER TABLE lead ADD COLUMN ctwa_clid text;

-- dedup de ingestao (fase 11) casa por telefone + empreendimento dentro de
-- uma janela -- sem indice, a query de dedup varreria a tabela inteira a
-- cada lead novo.
CREATE INDEX lead_telefone_empreendimento_idx ON lead (telefone_e164, empreendimento_id);

-- +goose Down
DROP INDEX lead_telefone_empreendimento_idx;
ALTER TABLE lead DROP COLUMN ctwa_clid;
ALTER TABLE lead DROP COLUMN ad_source_id;
