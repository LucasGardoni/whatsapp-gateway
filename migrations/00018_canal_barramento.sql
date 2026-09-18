-- +goose Up

-- Barramento, fase 4 (docs/PLANO_BARRAMENTO_MENSAGENS.md, secao 5.2).
--
-- Esta e a UNICA estrutura de roteamento que o gateway precisa, e a unica
-- coisa que nao da para empurrar para a aplicacao: alguem tem que saber
-- que uma mensagem de canal vai para N conexoes. As alternativas foram
-- avaliadas na secao 5.2 -- mandar a lista a cada envio deixaria o
-- gateway sem estado, mas quem entra depois nao teria historico e entrega
-- a quem esta offline ficaria impossivel.
--
-- canal_externo e OPACO: o dono do significado e a aplicacao. O gateway
-- nao sabe se 'setor-comercial' e um setor, uma thread ou uma fila, e nao
-- pode passar a saber -- e isso que impede a regra de negocio vazar para
-- ca (secao 2).
CREATE TABLE canal (
    id            bigserial PRIMARY KEY,
    aplicacao_id  bigint NOT NULL REFERENCES aplicacao (id),
    canal_externo text NOT NULL,
    criado_em     timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    UNIQUE (aplicacao_id, canal_externo)
);

-- A UNIQUE acima e o escopo por aplicacao virando schema: duas aplicacoes
-- podem usar o mesmo canal_externo sem colidir, e nenhuma consegue
-- enxergar o canal da outra nem por engano de query.

-- Lista de entrega. Note o que esta tabela NAO tem: papel, permissao,
-- hierarquia ou validade. E lista de strings sem significado. Quem decide
-- quem entra nela e a aplicacao -- se um dia aparecer aqui uma coluna que
-- o gateway precise interpretar, o modelo foi quebrado (secao 5.2).
CREATE TABLE canal_assinante (
    canal_id        bigint NOT NULL REFERENCES canal (id) ON DELETE CASCADE,
    destino_externo text NOT NULL,
    criado_em       timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    PRIMARY KEY (canal_id, destino_externo)
);

-- ON DELETE CASCADE acima: apagar um canal nao pode deixar assinante orfao
-- apontando para id reciclado -- seria entrega para o destino errado, que
-- e a falha mais cara que esta tabela pode ter.

-- a PK ja cobre a busca por canal; este indice cobre o caminho inverso
-- ("em que canais este destino esta"), usado para desassinar alguem de
-- tudo quando sai da empresa.
CREATE INDEX canal_assinante_destino_idx ON canal_assinante (destino_externo);

-- +goose Down
DROP TABLE canal_assinante;
DROP TABLE canal;
