-- +goose Up

-- G6 (docs/PLANO_MULTICAIXA_E_CONVERSAS.md): marcacao de leitura por
-- aplicacao. E por aplicacao, e nao global na conversa, porque o CRM e o
-- portal leem a mesma caixa e um nao pode zerar o badge do outro.
--
-- Guarda so o maior id lido: nao_lidas e contagem de entrada acima dele,
-- sem uma linha por mensagem.
CREATE TABLE conversa_leitura (
    aplicacao_id      bigint    NOT NULL REFERENCES aplicacao (id),
    conversa_id       bigint    NOT NULL REFERENCES conversa (id),
    ultima_lida_id    bigint    NOT NULL,
    atualizado_em     timestamp NOT NULL DEFAULT now(),
    PRIMARY KEY (aplicacao_id, conversa_id)
);

-- +goose Down

DROP TABLE conversa_leitura;
