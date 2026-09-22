-- +goose Up

-- Barramento, fase 9 (docs/PLANO_BARRAMENTO_MENSAGENS.md).
--
-- Endurecimento por aplicacao. Tudo aqui e DADO, e isso nao e detalhe: a
-- secao 2, item 6 do plano proibe `if aplicacao == "portal"`, e permite
-- exatamente isto -- comportamento por aplicacao gravado em coluna. Um
-- limite diferente para o Portal e um UPDATE, nunca um deploy.
--
-- Os dois limites sao NULAVEIS de proposito: NULL significa "usa o
-- default do processo" (RATE_LIMIT_APLICACAO_POR_MINUTO e
-- LIMITE_CONTEUDO_CIFRADO_BYTES). Com DEFAULT numerico, cada aplicacao
-- nova nasceria com o valor congelado no dia da migration, e subir o
-- default deixaria de alcancar quem nunca foi configurado -- que e
-- justamente quem mais precisa do default.
ALTER TABLE aplicacao
    ADD COLUMN limite_requisicoes_por_minuto integer,
    ADD COLUMN limite_conteudo_cifrado_bytes integer,
    -- quem pode ler GET /metrics. Tambem e dado: a alternativa era uma
    -- credencial de operador separada, que reintroduziria o segredo
    -- unico compartilhado que a fase 8 acabou de remover.
    ADD COLUMN pode_ler_metricas boolean NOT NULL DEFAULT false;

-- zero e negativo nao tem leitura util aqui: zero significaria "bloqueie
-- tudo" num campo cujo vazio ja quer dizer "use o default", e o operador
-- que digitar 0 esperando desligar o limite desligaria o trafego inteiro
-- da aplicacao. Recusar na escrita deixa o erro no UPDATE, e nao numa
-- madrugada de 429.
ALTER TABLE aplicacao
    ADD CONSTRAINT aplicacao_limite_requisicoes_positivo
        CHECK (limite_requisicoes_por_minuto IS NULL OR limite_requisicoes_por_minuto > 0),
    ADD CONSTRAINT aplicacao_limite_conteudo_positivo
        CHECK (limite_conteudo_cifrado_bytes IS NULL OR limite_conteudo_cifrado_bytes > 0);

-- o CRM e hoje o painel de supervisao da empresa (fases 9 e 11 do plano
-- original), entao e ele que le as metricas. As aplicacoes que entrarem
-- depois nascem sem o direito, e ganha-lo e um UPDATE consciente.
UPDATE aplicacao SET pode_ler_metricas = true WHERE codigo = 'crm';

-- Alerta passa a ter dono (fase 9, "alerta quando uma aplicacao passa de
-- N x a media").
--
-- Nulavel porque o alerta de volume que ja existe -- destinatarios
-- distintos no WhatsApp, fase 12 do plano original -- e da empresa, nao
-- de uma aplicacao: ele mede risco de banimento do numero, que e
-- compartilhado.
--
-- Sem esta coluna, o debounce de BuscarAlertaRecente (que filtra por
-- tipo) faria o primeiro alerta de UMA aplicacao calar o alerta de todas
-- as outras pela janela inteira -- o caso em que mais importa saber.
ALTER TABLE alerta ADD COLUMN aplicacao_id bigint REFERENCES aplicacao (id);

CREATE INDEX alerta_tipo_aplicacao_idx ON alerta (tipo, aplicacao_id, criado_em DESC);

-- a medicao de volume por aplicacao varre mensagem_interna por
-- (aplicacao_id, criado_em) a cada ciclo. Sem este indice a varredura e
-- sequencial na tabela que mais cresce no barramento.
CREATE INDEX mensagem_interna_aplicacao_criado_em_idx
    ON mensagem_interna (aplicacao_id, criado_em);

-- +goose Down

DROP INDEX mensagem_interna_aplicacao_criado_em_idx;
DROP INDEX alerta_tipo_aplicacao_idx;
ALTER TABLE alerta DROP COLUMN aplicacao_id;

ALTER TABLE aplicacao
    DROP CONSTRAINT aplicacao_limite_conteudo_positivo,
    DROP CONSTRAINT aplicacao_limite_requisicoes_positivo;

ALTER TABLE aplicacao
    DROP COLUMN pode_ler_metricas,
    DROP COLUMN limite_conteudo_cifrado_bytes,
    DROP COLUMN limite_requisicoes_por_minuto;
