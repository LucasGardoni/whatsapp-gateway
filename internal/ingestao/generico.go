package ingestao

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
)

// payloadGenerico e o formato aceito pela origem "generico" -- para
// integrações simples (formulário de landing page própria, por exemplo)
// que não têm um formato de terceiro pra mapear.
type payloadGenerico struct {
	Nome             string `json:"nome"`
	Telefone         string `json:"telefone"`
	EmpreendimentoID *int64 `json:"empreendimento_id"`
}

// NormalizarGenerico aceita telefone com ou sem máscara/DDI -- passa pela
// mesma normalização E.164 usada no matcher (fase 4), então o dedup casa
// mesmo que a origem mande formatos diferentes.
func NormalizarGenerico(payload []byte) (Entrada, error) {
	var p payloadGenerico
	if err := json.Unmarshal(payload, &p); err != nil {
		return Entrada{}, fmt.Errorf("normalizar generico: %w", err)
	}

	p.Nome = strings.TrimSpace(p.Nome)
	if p.Telefone == "" {
		return Entrada{}, fmt.Errorf("normalizar generico: telefone ausente")
	}

	telefone, err := identidade.NormalizarE164(p.Telefone)
	if err != nil {
		return Entrada{}, fmt.Errorf("normalizar generico: %w", err)
	}

	return Entrada{
		Nome:             p.Nome,
		TelefoneE164:     telefone,
		Origem:           "generico",
		EmpreendimentoID: p.EmpreendimentoID,
	}, nil
}
