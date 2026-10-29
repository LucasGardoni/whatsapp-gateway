-- +goose Up

-- Encaminhar mensagem (forward-message da z-api). A mensagem nova copia
-- tipo, texto e midia da original -- e isso que entra no hash, entao a
-- cadeia prova o que foi encaminhado. encaminhada_de_id e o elo com a
-- original, de onde o outbox tira o messageId e o chat de origem; fica
-- fora do hash, como responde_a_id.
ALTER TABLE mensagem ADD COLUMN encaminhada_de_id bigint REFERENCES mensagem (id);

-- +goose Down

ALTER TABLE mensagem DROP COLUMN encaminhada_de_id;
