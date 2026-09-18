-- +goose Up

-- CRM de corretores (docs/plano-dev-crm-corretores.md, PHP): a tela "Hoje"
-- (E4) precisa ordenar por urgencia usando estagio comercial e valor do
-- lead, nenhum dos dois existia em lugar nenhum (nem aqui, nem no PHP) --
-- levantamento registrado no log da E3/E4 daquele plano. `lead.estado`
-- (00002_lead.sql) e o funil de ENGAJAMENTO DE DISPARO (novo/disparado/
-- clicou/engajado/em_atendimento/ganho/perdido), continua existindo do
-- jeito que esta e nao e alterado por esta migration -- e o funil COMERCIAL
-- de venda, deliberadamente separado (mesma logica de "gateway nao tem
-- tabela empreendimento" do comentario em 00007_disparo_empreendimento.sql).
--
-- Nao populamos parametro com SLA por estagio aqui -- o fallback fica no
-- lado PHP (mesmo padrao do fallback global de SlaConfig::minutosParaOrigem),
-- e pode ser sobrescrito via a mesma tabela `parametro` chave/valor que ja
-- existe (00005_midia_saude_parametro.sql), com a chave
-- `sla_minutos_estagio:{estagio}` -- sem precisar de tabela nova.
ALTER TABLE lead
    ADD COLUMN estagio_comercial text NOT NULL DEFAULT 'novo'
        CHECK (estagio_comercial IN ('novo', 'contato', 'visita', 'proposta',
                                      'fechamento', 'ganho', 'perdido')),
    ADD COLUMN estagio_entrou_em timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    ADD COLUMN valor numeric(14, 2);

CREATE INDEX lead_estagio_comercial_idx ON lead (estagio_comercial);

-- +goose Down
DROP INDEX IF EXISTS lead_estagio_comercial_idx;
ALTER TABLE lead
    DROP COLUMN valor,
    DROP COLUMN estagio_entrou_em,
    DROP COLUMN estagio_comercial;
