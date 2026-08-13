-- +goose Up

-- P1-13: nada impedia duas conversas abertas para o mesmo lead. Quando
-- acontece, BuscarConversaAbertaPorLead (ORDER BY aberta_em DESC LIMIT 1)
-- escolhe uma delas e o historico do cliente fica partido em duas telas --
-- o corretor responde numa e a mensagem seguinte aparece na outra.
--
-- O indice unico parcial e a garantia. Antes dele, fechar as duplicatas que
-- ja existem, senao a migration falha em qualquer banco com dado real
-- (bloco 10.1 do docs/verificacao-schema.sql).
--
-- Criterio: mantem a conversa MAIS RECENTE aberta (e onde as mensagens
-- novas estao chegando) e fecha as anteriores. Fechar nao apaga nada: as
-- mensagens seguem ligadas a sua conversa e continuam consultaveis.
UPDATE conversa c
SET fechada_em = LOCALTIMESTAMP
WHERE c.fechada_em IS NULL
  AND EXISTS (
      SELECT 1 FROM conversa mais_recente
      WHERE mais_recente.lead_id = c.lead_id
        AND mais_recente.fechada_em IS NULL
        AND (mais_recente.aberta_em, mais_recente.id) > (c.aberta_em, c.id)
  );

CREATE UNIQUE INDEX conversa_lead_aberta_idx
    ON conversa (lead_id)
    WHERE fechada_em IS NULL;

-- indices das consultas quentes (Fase 1 da auditoria). Todas essas rodam em
-- loop: o outbox a cada ciclo, o monitor de saude a cada 30s, a tela de
-- conversa a cada polling.

-- outbox: SelecionarPendentesParaEnvio filtra direcao+status a cada ciclo.
CREATE INDEX mensagem_direcao_status_idx ON mensagem (direcao, status);

-- tela de conversa: mensagens de uma conversa em ordem cronologica.
CREATE INDEX mensagem_conversa_criado_em_idx ON mensagem (conversa_id, criado_em);

-- monitor de saude: sempre le a leitura mais recente de um provedor.
CREATE INDEX provedor_saude_provedor_verificado_em_idx
    ON provedor_saude (provedor, verificado_em DESC);

-- fila/SLA: o prazo de um lead sai do evento mais recente de um tipo.
CREATE INDEX sla_evento_lead_tipo_ocorrido_em_idx
    ON sla_evento (lead_id, tipo, ocorrido_em);

-- +goose Down

DROP INDEX IF EXISTS sla_evento_lead_tipo_ocorrido_em_idx;
DROP INDEX IF EXISTS provedor_saude_provedor_verificado_em_idx;
DROP INDEX IF EXISTS mensagem_conversa_criado_em_idx;
DROP INDEX IF EXISTS mensagem_direcao_status_idx;
DROP INDEX IF EXISTS conversa_lead_aberta_idx;
-- as conversas fechadas pelo Up nao sao reabertas: nao ha como distinguir
-- as que esta migration fechou das que foram fechadas pela operacao.
