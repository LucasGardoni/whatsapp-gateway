-- +goose Up

-- Responder citando, editar e excluir (docs do portal: PLANO_CRM_EDITAR_EXCLUIR_RESPONDER).
--
-- mensagem.texto continua sendo o que foi enviado/recebido: ele entra no hash
-- encadeado, e um UPDATE nele quebraria a verificacao da cadeia. Edicao e
-- exclusao viram linhas em mensagem_alteracao, com elo proprio na MESMA
-- cadeia das mensagens (auditoria_ultimo_hash), para a ordem global ficar
-- provada. As colunas novas de mensagem sao atalho de leitura e nao entram
-- no hash.

-- quem enviou pela aplicacao ('usuario:808'); nulo na entrada e no que veio
-- antes desta migration.
ALTER TABLE mensagem ADD COLUMN autor text;
ALTER TABLE mensagem ADD COLUMN responde_a_id bigint REFERENCES mensagem (id);
ALTER TABLE mensagem ADD COLUMN editada_em timestamp;
ALTER TABLE mensagem ADD COLUMN excluida_em timestamp;
-- 'usuario:808' | 'cliente'
ALTER TABLE mensagem ADD COLUMN excluida_por text;

CREATE TABLE mensagem_alteracao (
    id            bigserial PRIMARY KEY,
    mensagem_id   bigint NOT NULL REFERENCES mensagem (id),
    acao          text NOT NULL CHECK (acao IN ('edicao', 'exclusao')),
    -- texto NOVO na edicao; nulo na exclusao
    texto         text,
    -- 'usuario:808' | 'cliente'
    autor         text NOT NULL,
    criado_em     timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    -- id que o provedor deu a propria notificacao de edicao/revogacao
    provedor_msg_id text,
    payload_bruto jsonb,
    hash_anterior text,
    hash          text
);

CREATE INDEX mensagem_alteracao_mensagem_idx ON mensagem_alteracao (mensagem_id, id);

-- +goose Down

DROP TABLE mensagem_alteracao;
ALTER TABLE mensagem DROP COLUMN excluida_por;
ALTER TABLE mensagem DROP COLUMN excluida_em;
ALTER TABLE mensagem DROP COLUMN editada_em;
ALTER TABLE mensagem DROP COLUMN responde_a_id;
ALTER TABLE mensagem DROP COLUMN autor;
