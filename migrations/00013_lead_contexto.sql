-- +goose Up

-- CRM de corretores, E7 (docs/plano-dev-crm-corretores.md): "Contexto do
-- lead" precisa de empreendimento (nome, não só o id que já existia),
-- tipologia, forma de pagamento, próximo passo agendado e notas — nenhum
-- dos cinco existia em lugar nenhum (levantamento repetido no log da
-- E0/E5 daquele plano). `lead.empreendimento_id` (00002_lead.sql) segue
-- existindo do jeito que está; `nome_empreendimento` aqui é texto solto,
-- não FK — mesma decisão já registrada em
-- `00007_disparo_empreendimento.sql` ("gateway não tem tabela
-- empreendimento, mora no crm/protheus, fora de escopo"; o campo aqui é
-- só o que o corretor digita/vê, igual ao `disparo.nome_empreendimento`).
ALTER TABLE lead
    ADD COLUMN nome_empreendimento text,
    ADD COLUMN tipologia           text,
    ADD COLUMN forma_pagamento     text,
    ADD COLUMN proximo_passo_texto text,
    ADD COLUMN proximo_passo_em    timestamp,
    ADD COLUMN notas               text;

-- +goose Down
ALTER TABLE lead
    DROP COLUMN notas,
    DROP COLUMN proximo_passo_em,
    DROP COLUMN proximo_passo_texto,
    DROP COLUMN forma_pagamento,
    DROP COLUMN tipologia,
    DROP COLUMN nome_empreendimento;
