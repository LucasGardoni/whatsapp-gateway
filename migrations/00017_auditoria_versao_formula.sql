-- +goose Up

-- Barramento, fase 2: a cadeia de auditoria passa a encadear tambem a
-- ORIGEM da mensagem (docs/PLANO_BARRAMENTO_MENSAGENS.md, secao 8 fase 2).
--
-- Isso QUEBRA a continuidade da cadeia existente de proposito: hash
-- calculado pela formula nova nao e verificavel pela antiga, e vice-versa.
-- Nao ha recalculo do historico -- reescrever hash antigo e exatamente o
-- que a cadeia existe para impedir. O que se faz e registrar onde fica o
-- corte, para que a verificacao futura saiba qual formula aplicar a cada
-- trecho.
--
-- O corte e por ID DE MENSAGEM, e nao por data: o id e monotonico e
-- inequivoco, enquanto uma data de corte deixaria em duvida toda mensagem
-- gravada no mesmo segundo do deploy. A data fica na descricao, para
-- leitura humana.
--
-- A versao 1 e a formula sem origem:
--   sha256(anterior |id|conversa_id|direcao|tipo|texto|midia_caminho|provedor_msg_id)
-- A versao 2 acrescenta origem no fim:
--   sha256(anterior |id|conversa_id|direcao|tipo|texto|midia_caminho|provedor_msg_id|origem)
INSERT INTO parametro (chave, valor, descricao)
VALUES (
    'auditoria_versao_formula',
    '2',
    'versao da formula do hash de auditoria (2 = com origem; ver internal/auditoria/hash.go)'
)
ON CONFLICT (chave) DO UPDATE SET valor = EXCLUDED.valor, descricao = EXCLUDED.descricao;

-- Calculado no momento em que a migration roda: a ultima mensagem que ja
-- tinha hash e, por definicao, a ultima da formula 1. Tudo com id maior
-- nasce sob a formula 2.
--
-- Depende da migration rodar ANTES do binario novo subir -- que e a ordem
-- normal de deploy. Se o binario novo subir antes, as mensagens gravadas
-- nessa janela ficam com hash de formula 2 e id abaixo do corte, e a
-- verificacao precisa tentar as duas formulas nesse intervalo. Registrado
-- aqui porque o sintoma, seis meses depois, seria "a cadeia nao fecha" sem
-- nenhuma pista do motivo.
INSERT INTO parametro (chave, valor, descricao)
SELECT
    'auditoria_formula_2_a_partir_de_mensagem_id',
    (COALESCE(MAX(id), 0) + 1)::text,
    'primeira mensagem sob a formula 2; id menor que este usa a formula 1. Corte em ' ||
        to_char(LOCALTIMESTAMP, 'YYYY-MM-DD HH24:MI:SS')
FROM mensagem
WHERE hash IS NOT NULL
ON CONFLICT (chave) DO NOTHING;

-- +goose Down
DELETE FROM parametro WHERE chave IN (
    'auditoria_versao_formula',
    'auditoria_formula_2_a_partir_de_mensagem_id'
);
