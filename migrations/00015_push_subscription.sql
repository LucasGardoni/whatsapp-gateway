-- +goose Up

-- CRM de corretores (docs/plano-dev-crm-corretores.md, PHP), Etapa E10
-- (Notificacao e entrada): Web Push precisa persistir a subscription que
-- o navegador devolve (endpoint + chaves p256dh/auth) por corretor, pra
-- o lado PHP poder disparar push depois. Nao existe em lugar nenhum do
-- schema -- lacuna nova, fechada aqui, aditiva. Fica no mesmo Postgres
-- do Gateway (mesmo padrao ja usado desde a E4/E7/E8: schema de front de
-- corretores mora aqui, nao num banco/migration paralelo do lado PHP).
--
-- Um corretor pode ter mais de um dispositivo/navegador logado ao mesmo
-- tempo (celular + desktop), entao a chave e (corretor_id, endpoint), nao
-- so corretor_id -- UNIQUE evita subscription duplicada quando o
-- navegador re-registra o mesmo endpoint (Push API faz isso sozinha as
-- vezes, sem o usuario pedir).
CREATE TABLE push_subscription (
    id           bigserial PRIMARY KEY,
    corretor_id  bigint NOT NULL REFERENCES usuario(id),
    endpoint     text NOT NULL,
    p256dh       text NOT NULL,
    auth         text NOT NULL,
    criado_em    timestamp NOT NULL DEFAULT LOCALTIMESTAMP,
    UNIQUE (corretor_id, endpoint)
);

CREATE INDEX idx_push_subscription_corretor ON push_subscription (corretor_id);

-- +goose Down
DROP TABLE push_subscription;
