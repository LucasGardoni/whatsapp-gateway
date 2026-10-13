-- +goose Up

-- Mensagem automatica (URA, protocolo, despedida, pedido de nota): o contato
-- acabou de escrever e espera a resposta. O outbox manda sem o "digitando"
-- proporcional ao texto, que existe para o atendimento humano e o disparo.
ALTER TABLE mensagem ADD COLUMN automatica boolean NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE mensagem DROP COLUMN automatica;
