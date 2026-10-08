-- +goose Up

-- Figurinha (sticker) vira tipo proprio: recebida chegava como 'outro' e o
-- envio pela z-api usa /send-sticker, nao /send-image.
ALTER TABLE mensagem DROP CONSTRAINT IF EXISTS mensagem_tipo_check;
ALTER TABLE mensagem ADD CONSTRAINT mensagem_tipo_check
    CHECK (tipo IN ('texto', 'imagem', 'audio', 'video', 'documento', 'figurinha', 'outro'));

-- +goose Down

UPDATE mensagem SET tipo = 'outro' WHERE tipo = 'figurinha';
ALTER TABLE mensagem DROP CONSTRAINT IF EXISTS mensagem_tipo_check;
ALTER TABLE mensagem ADD CONSTRAINT mensagem_tipo_check
    CHECK (tipo IN ('texto', 'imagem', 'audio', 'video', 'documento', 'outro'));
