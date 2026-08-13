-- +goose Up
-- motivo do ultimo erro de envio (shadowban, falha do provedor etc) --
-- consultavel pelo supervisor sem precisar cruzar com provedor_saude,
-- que so guarda erro da instancia como um todo (fase 9).
ALTER TABLE mensagem ADD COLUMN ultimo_erro text;

-- +goose Down
ALTER TABLE mensagem DROP COLUMN ultimo_erro;
