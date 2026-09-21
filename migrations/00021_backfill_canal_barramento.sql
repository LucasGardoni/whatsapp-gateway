-- +goose Up

-- Barramento, fase 8 (docs/PLANO_BARRAMENTO_MENSAGENS.md).
--
-- Migra o chat interno do caminho legado para o barramento. O legado era:
-- o CRM escrevia direto em `mensagem_interna` (canal_id -> canal_interno,
-- usuario_id -> usuario, texto em claro) e o gateway descobria por
-- polling. O caminho novo e opaco: `canal` + `canal_assinante`, remetente
-- e canal como strings que o gateway nao interpreta, conteudo cifrado.
--
-- Esta migration faz a parte que E possivel em SQL. O que ela NAO faz, e
-- nao tem como fazer, e cifrar o `texto` das mensagens antigas: a chave
-- vive no backend do CRM e nunca passou por aqui -- que e exatamente a
-- propriedade que a fase 8 entrega. Isso fica para o comando
-- `php spark chat-interno:migrar-legado`, do lado do CRM, que roda DEPOIS
-- desta migration e ANTES da 00022 (que apaga as colunas legadas).
--
-- Ordem obrigatoria:
--   1. goose up ate aqui (00021)
--   2. php spark chat-interno:migrar-legado   (no CRM)
--   3. goose up ate 00022

-- +goose StatementBegin
DO $$
DECLARE
    app_crm bigint;
BEGIN
    SELECT id INTO app_crm FROM aplicacao WHERE codigo = 'crm';

    IF app_crm IS NULL THEN
        RAISE EXCEPTION 'aplicacao crm nao existe; rode o gateway uma vez antes desta migration';
    END IF;

    -- canal_interno vira canal opaco. A convencao 'ci-<id>' e a mesma do
    -- CanalInternoModel::canalExterno() no CRM; divergir aqui criaria
    -- canais duplicados na primeira vez que a tela abrisse.
    INSERT INTO canal (aplicacao_id, canal_externo)
    SELECT app_crm, 'ci-' || ci.id
    FROM canal_interno ci
    ON CONFLICT (aplicacao_id, canal_externo) DO NOTHING;

    -- Lista de entrega inicial: todo usuario ativo em canal de setor e de
    -- thread_lead (que eram abertos a qualquer perfil logado), e em DM
    -- apenas os dois participantes mais admin/supervisor.
    --
    -- O destino e o id do usuario em texto, e nao e escolha arbitraria: a
    -- chave de entrega do hub e 'crm:<destino>', a mesma que os eventos
    -- de WhatsApp ja usavam. Assim uma conexao SSE so recebe os dois.
    INSERT INTO canal_assinante (canal_id, destino_externo)
    SELECT c.id, u.id::text
    FROM canal_interno ci
    JOIN canal c ON c.aplicacao_id = app_crm AND c.canal_externo = 'ci-' || ci.id
    CROSS JOIN usuario u
    WHERE u.ativo
      AND (
            ci.tipo <> 'dm'
            OR u.perfil IN ('admin', 'supervisor')
            OR ci.nome ~ ('(^|:)' || u.id::text || '(:|$)')
          )
    ON CONFLICT (canal_id, destino_externo) DO NOTHING;

    -- As mensagens antigas apontam para canal_interno; passam a apontar
    -- para o canal opaco. O CHECK `um_canal_apenas` exige exatamente uma
    -- das duas colunas preenchida, por isso canal_id zera na mesma linha.
    UPDATE mensagem_interna mi
    SET canal_ref_id      = c.id,
        canal_id          = NULL,
        aplicacao_id      = app_crm,
        remetente_externo = mi.usuario_id::text
    FROM canal c
    WHERE mi.canal_id IS NOT NULL
      AND c.aplicacao_id = app_crm
      AND c.canal_externo = 'ci-' || mi.canal_id;
END
$$;
-- +goose StatementEnd

-- Procedencia deixa de ser opcional: ate a fase 7 uma mensagem podia
-- nascer sem aplicacao porque o token de servico unico nao identificava
-- quem chamava. Na fase 8 esse token acabou -- toda escrita vem de uma
-- aplicacao identificada, entao a coluna que registra isso pode finalmente
-- exigir valor.
ALTER TABLE mensagem_interna ALTER COLUMN aplicacao_id SET NOT NULL;

-- +goose StatementBegin
DO $$
DECLARE
    orfas bigint;
BEGIN
    SELECT count(*) INTO orfas FROM mensagem WHERE aplicacao_id IS NULL;

    IF orfas > 0 THEN
        -- mensagem de WhatsApp anterior a fase 1 nao tem de onde herdar
        -- procedencia: o unico consumidor que existia era o CRM.
        UPDATE mensagem SET aplicacao_id = (SELECT id FROM aplicacao WHERE codigo = 'crm')
        WHERE aplicacao_id IS NULL;
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE mensagem ALTER COLUMN aplicacao_id SET NOT NULL;

-- +goose Down

ALTER TABLE mensagem ALTER COLUMN aplicacao_id DROP NOT NULL;
ALTER TABLE mensagem_interna ALTER COLUMN aplicacao_id DROP NOT NULL;

-- +goose StatementBegin
DO $$
DECLARE
    app_crm bigint;
BEGIN
    SELECT id INTO app_crm FROM aplicacao WHERE codigo = 'crm';

    -- devolve as mensagens ao canal_interno de origem, lendo o id de
    -- volta do proprio canal_externo.
    UPDATE mensagem_interna mi
    SET canal_id     = substring(c.canal_externo from 4)::bigint,
        canal_ref_id = NULL
    FROM canal c
    WHERE mi.canal_ref_id = c.id
      AND c.aplicacao_id = app_crm
      AND c.canal_externo LIKE 'ci-%';

    DELETE FROM canal_assinante WHERE canal_id IN (
        SELECT id FROM canal WHERE aplicacao_id = app_crm AND canal_externo LIKE 'ci-%'
    );
    DELETE FROM canal WHERE aplicacao_id = app_crm AND canal_externo LIKE 'ci-%';
END
$$;
-- +goose StatementEnd
