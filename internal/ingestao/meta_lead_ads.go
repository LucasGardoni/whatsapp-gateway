package ingestao

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
)

// payloadMetaLeadAds e o formato do objeto de lead da Graph API --
// exatamente o que "GET /{leadgen_id}?fields=field_data" devolve, e
// também o formato que a maioria das integrações no-code (Zapier/Make)
// entrega direto no webhook de destino.
//
// O webhook nativo da Meta (`page` -> `leadgen`) manda só
// `{"entry":[{"changes":[{"value":{"leadgen_id":"..."}}]}]}` -- sem os
// dados do formulário, que exigem uma segunda chamada autenticada na
// Graph API com um token de página (permissão leads_retrieval). Esse
// token não está em nenhuma variável de config deste projeto (só existe
// META_ACCESS_TOKEN da Cloud API, com escopo diferente) e não foi pedido
// no plano -- então esse normalizador não faz esse segundo passo. Se o
// leadgen_id chegar sem field_data, ele falha com um erro claro em vez de
// inventar a integração; o payload bruto já foi persistido antes disso
// (diretriz 7), então nada se perde -- só não vira lead automaticamente
// até essa peça ser decidida.
type payloadMetaLeadAds struct {
	FieldData []campoMetaLeadAds `json:"field_data"`
	AdID      string             `json:"ad_id"`
	FormID    string             `json:"form_id"`
	Entry     []struct {
		Changes []struct {
			Value struct {
				LeadgenID string `json:"leadgen_id"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type campoMetaLeadAds struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

func NormalizarMetaLeadAds(payload []byte) (Entrada, error) {
	var p payloadMetaLeadAds
	if err := json.Unmarshal(payload, &p); err != nil {
		return Entrada{}, fmt.Errorf("normalizar meta lead ads: %w", err)
	}

	if len(p.FieldData) == 0 {
		if leadgenID := extrairLeadgenID(p); leadgenID != "" {
			return Entrada{}, fmt.Errorf("normalizar meta lead ads: leadgen_id %s sem field_data -- webhook nativo da meta exige busca na graph api, nao implementada (ver comentario do tipo payloadMetaLeadAds)", leadgenID)
		}
		return Entrada{}, fmt.Errorf("normalizar meta lead ads: payload sem field_data")
	}

	var nome, telefoneBruto string
	for _, campo := range p.FieldData {
		if len(campo.Values) == 0 {
			continue
		}
		switch campo.Name {
		case "full_name":
			nome = campo.Values[0]
		case "first_name":
			nome = strings.TrimSpace(nome + " " + campo.Values[0])
		case "last_name":
			nome = strings.TrimSpace(nome + " " + campo.Values[0])
		case "phone_number":
			telefoneBruto = campo.Values[0]
		}
	}

	if telefoneBruto == "" {
		return Entrada{}, fmt.Errorf("normalizar meta lead ads: field_data sem phone_number")
	}

	telefone, err := identidade.NormalizarE164(telefoneBruto)
	if err != nil {
		return Entrada{}, fmt.Errorf("normalizar meta lead ads: %w", err)
	}

	return Entrada{
		Nome:         nome,
		TelefoneE164: telefone,
		Origem:       "meta-lead-ads",
		AdSourceID:   p.AdID,
	}, nil
}

func extrairLeadgenID(p payloadMetaLeadAds) string {
	for _, e := range p.Entry {
		for _, c := range e.Changes {
			if c.Value.LeadgenID != "" {
				return c.Value.LeadgenID
			}
		}
	}
	return ""
}
