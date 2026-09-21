-- +goose Up

-- Barramento, fase 7 (docs/PLANO_BARRAMENTO_MENSAGENS.md, secao 7.2).
--
-- Ate aqui o tempo real so funcionava com UMA instancia do gateway: quem
-- gravava a mensagem publicava direto no hub em memoria do proprio
-- processo, e uma segunda instancia nunca ficava sabendo. `evento` e o
-- ponto de encontro entre instancias.
--
-- A secao 7.2 previa `pg_notify` carregando ids e cada instancia
-- resolvendo os assinantes em `canal_assinante` depois do commit. Esta
-- tabela substitui aquele desenho, por dois motivos que so apareceram na
-- implementacao:
--
--  1. Resolver a lista DEPOIS do commit reabre a janela que a fase 5
--     fechou de proposito (decisao 7): quem sai do canal no mesmo
--     instante voltaria a receber. Aqui a lista e gravada DENTRO da
--     transacao que escreve a mensagem, exatamente como hoje.
--  2. `NOTIFY` perdido nao e reentregue, e a secao 7.2 exige catch-up.
--     Evento de status de WhatsApp nao tem de onde ser reconstruido --
--     `mensagem.status` guarda o estado atual, nao a transicao. Com a
--     tabela, o catch-up e uma so consulta `id > ultimo_visto` para todos
--     os tipos de evento.
CREATE TABLE evento (
    id        bigserial PRIMARY KEY,
    -- chaves de entrega do hub ("aplicacao:destino", ver sse.ChaveDestino),
    -- ja resolvidas por quem gravou. Array vazio e valido e significa
    -- "gravou e nao ha ninguem a avisar" -- o evento fica na trilha, mas
    -- nao sai para ninguem.
    chaves    text[] NOT NULL,
    -- aplicacao preenchida entrega a TODOS os destinos de uma aplicacao.
    -- E o que sobrou do broadcast antigo e existe so pela fila de espera
    -- do CRM, que por definicao nao tem destinatario listado (ver
    -- sse.PublicarNaAplicacao). Some na fase 8.
    aplicacao text,
    -- o sse.Evento serializado. NUNCA carrega conteudo, nem cifrado
    -- (decisao fechada 5): avisa que algo mudou, e a aplicacao busca.
    payload   jsonb NOT NULL,
    criado_em timestamptz NOT NULL DEFAULT now()
);

-- a limpeza (evento e roteamento efemero, nao historico) varre por idade.
CREATE INDEX evento_criado_em_idx ON evento (criado_em);

-- +goose StatementBegin
CREATE FUNCTION evento_notificar() RETURNS trigger AS $$
BEGIN
    -- so o id: o payload de NOTIFY tem teto de 8000 bytes e uma lista de
    -- chaves de canal grande estouraria. Quem escuta busca a linha.
    --
    -- O gatilho existe para que publicar seja IMPOSSIVEL de esquecer:
    -- gravar a linha ja e notificar, e nao ha o par "insere e esquece o
    -- pg_notify" espalhado por seis lugares. Dispara no commit, entao
    -- ninguem e avisado de transacao que acabou descartada.
    PERFORM pg_notify('gw_evento', NEW.id::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER evento_notificar_apos_insert
    AFTER INSERT ON evento
    FOR EACH ROW EXECUTE FUNCTION evento_notificar();

-- +goose Down
DROP TRIGGER evento_notificar_apos_insert ON evento;
DROP FUNCTION evento_notificar();
DROP TABLE evento;
