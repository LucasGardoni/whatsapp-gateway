-- +goose Up

-- CRM de corretores (docs/plano-dev-crm-corretores.md, PHP), Etapa E8
-- (Estagio e funil): "Ganho e perda com confirmacao e motivo obrigatorio
-- na perda". `sla_evento` (00004_dlp_sla.sql) so tem `tipo`, sem lugar
-- pra guardar o motivo digitado pelo corretor -- lacuna nova, fechada
-- aqui, aditiva, sem tocar em nenhuma coluna existente. `detalhe` fica
-- NULL pra todo evento que nao precisa de texto livre (atribuicao,
-- transferencia, etc.) -- so a mudanca de estagio (e a perda, dentro
-- dela) usa.
ALTER TABLE sla_evento
    ADD COLUMN detalhe text;

-- +goose Down
ALTER TABLE sla_evento
    DROP COLUMN detalhe;
