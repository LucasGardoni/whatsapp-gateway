-- +goose Up

-- Contato (vCard) vira tipo proprio: recebido chegava como 'outro', sem
-- previa, e o envio pela z-api usa /send-contact. Em mensagem.texto fica o
-- nome na primeira linha e um telefone por linha depois (mensagem.Contato).
ALTER TABLE mensagem DROP CONSTRAINT IF EXISTS mensagem_tipo_check;
ALTER TABLE mensagem ADD CONSTRAINT mensagem_tipo_check
    CHECK (tipo IN ('texto', 'imagem', 'audio', 'video', 'documento', 'figurinha', 'contato', 'outro'));

-- +goose Down

UPDATE mensagem SET tipo = 'outro' WHERE tipo = 'contato';
ALTER TABLE mensagem DROP CONSTRAINT IF EXISTS mensagem_tipo_check;
ALTER TABLE mensagem ADD CONSTRAINT mensagem_tipo_check
    CHECK (tipo IN ('texto', 'imagem', 'audio', 'video', 'documento', 'figurinha', 'outro'));
