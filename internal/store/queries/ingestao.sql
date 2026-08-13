-- name: BuscarLeadRecenteParaDedup :one
-- dedup da fase 11: mesmo telefone + mesmo empreendimento dentro da janela
-- configuravel e o mesmo lead, nao um novo. empreendimento_id nulo em
-- ambos os lados (IS NOT DISTINCT FROM) tambem casa -- origem sem
-- empreendimento identificado ainda conta como duplicata pelo telefone.
SELECT * FROM lead
WHERE telefone_e164 = $1
  AND empreendimento_id IS NOT DISTINCT FROM $2
  AND criado_em >= $3
ORDER BY criado_em DESC
LIMIT 1;

-- name: CriarLeadDeIngestao :one
-- variante de CriarLead (recepcao.sql) que tambem grava empreendimento e
-- atribuicao de campanha, conhecidos no momento da ingestao (fase 11) --
-- chat_lid so chega depois, na primeira mensagem de whatsapp (secao 4.3).
INSERT INTO lead (nome, telefone_e164, origem, empreendimento_id, ad_source_id, ctwa_clid, estado)
VALUES ($1, $2, $3, $4, $5, $6, 'novo')
RETURNING *;
