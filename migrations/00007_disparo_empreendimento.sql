-- +goose Up
-- gateway nao tem tabela empreendimento (mora no crm/protheus, fora de
-- escopo). nome_empreendimento e so o que o crm manda ao criar o disparo,
-- guardado aqui pra pagina de /c/{token} poder renderizar sem depender de
-- outra base (decisao da fase 5).
ALTER TABLE disparo ADD COLUMN nome_empreendimento text;

-- +goose Down
ALTER TABLE disparo DROP COLUMN nome_empreendimento;
