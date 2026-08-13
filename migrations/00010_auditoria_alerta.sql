-- +goose Up
-- semente da cadeia de hash (fase 12, secao 2 defesa 4) -- garante que a
-- linha sempre existe, entao SELECT ... FOR UPDATE tem o que travar mesmo
-- antes da primeira mensagem (senao duas transacoes concorrentes na
-- primeira mensagem poderiam ler "sem linha" ao mesmo tempo e colidir).
INSERT INTO parametro (chave, valor, descricao)
VALUES ('auditoria_ultimo_hash', NULL, 'ultimo hash da cadeia de auditoria de mensagem (fase 12)')
ON CONFLICT (chave) DO NOTHING;

-- alerta generico -- comeca com volume anormal de destinatarios distintos
-- (fase 12, secao 4.8 fator no 1 de banimento), mas a tabela nao e
-- especifica desse tipo pra nao precisar de migration nova a cada alerta
-- futuro. Notificacao de verdade (e-mail, escalonamento) e o CRM que faz
-- lendo daqui -- mesmo padrao de provedor_saude/dlp_ocorrencia/sla_evento.
CREATE TABLE alerta (
    id        bigserial PRIMARY KEY,
    tipo      text NOT NULL,
    detalhe   text,
    criado_em timestamp NOT NULL DEFAULT LOCALTIMESTAMP
);

CREATE INDEX alerta_tipo_criado_em_idx ON alerta (tipo, criado_em);

-- +goose Down
DROP TABLE alerta;
DELETE FROM parametro WHERE chave = 'auditoria_ultimo_hash';
