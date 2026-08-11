// package cloudapi envia templates de disparo pelo numero A (Meta Cloud
// API). Nao atende cliente -- ver secao 3 do plano, numero A nunca faz
// atendimento humano.
package cloudapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const baseURLPadrao = "https://graph.facebook.com/v21.0"

type Cliente struct {
	baseURL       string
	phoneNumberID string
	accessToken   string
	http          *http.Client
}

func NovoCliente(phoneNumberID, accessToken string) *Cliente {
	return novoClienteComBase(baseURLPadrao, phoneNumberID, accessToken)
}

func novoClienteComBase(baseURL, phoneNumberID, accessToken string) *Cliente {
	return &Cliente{
		baseURL:       baseURL,
		phoneNumberID: phoneNumberID,
		accessToken:   accessToken,
		http:          http.DefaultClient,
	}
}

type ResultadoEnvio struct {
	MessageID string
}

type requisicaoTemplate struct {
	MessagingProduct string        `json:"messaging_product"`
	To               string        `json:"to"`
	Type             string        `json:"type"`
	Template         templateEnvio `json:"template"`
}

type templateEnvio struct {
	Name       string            `json:"name"`
	Language   idiomaEnvio       `json:"language"`
	Components []componenteEnvio `json:"components,omitempty"`
}

type idiomaEnvio struct {
	Code string `json:"code"`
}

type componenteEnvio struct {
	Type       string           `json:"type"`
	Parameters []parametroEnvio `json:"parameters"`
}

type parametroEnvio struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type respostaTemplate struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
}

// EnviarTemplate dispara um template ja aprovado pela Meta. parametros
// preenche as variaveis do corpo, na ordem declarada no template.
func (c *Cliente) EnviarTemplate(ctx context.Context, telefone, nomeTemplate, idiomaCodigo string, parametros []string) (*ResultadoEnvio, error) {
	requisicao := requisicaoTemplate{
		MessagingProduct: "whatsapp",
		To:               telefone,
		Type:             "template",
		Template: templateEnvio{
			Name:     nomeTemplate,
			Language: idiomaEnvio{Code: idiomaCodigo},
		},
	}

	if len(parametros) > 0 {
		params := make([]parametroEnvio, len(parametros))
		for i, p := range parametros {
			params[i] = parametroEnvio{Type: "text", Text: p}
		}
		requisicao.Template.Components = []componenteEnvio{{Type: "body", Parameters: params}}
	}

	corpo, err := json.Marshal(requisicao)
	if err != nil {
		return nil, fmt.Errorf("enviar template %s: %w", nomeTemplate, err)
	}

	url := fmt.Sprintf("%s/%s/messages", c.baseURL, c.phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(corpo))
	if err != nil {
		return nil, fmt.Errorf("enviar template %s: %w", nomeTemplate, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enviar template %s: %w", nomeTemplate, err)
	}
	defer resp.Body.Close()

	corpoResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("enviar template %s: ler resposta: %w", nomeTemplate, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enviar template %s: status %d da meta: %s", nomeTemplate, resp.StatusCode, corpoResp)
	}

	var r respostaTemplate
	if err := json.Unmarshal(corpoResp, &r); err != nil {
		return nil, fmt.Errorf("enviar template %s: decodificar resposta: %w", nomeTemplate, err)
	}
	if len(r.Messages) == 0 {
		return nil, fmt.Errorf("enviar template %s: resposta sem messageId", nomeTemplate)
	}

	return &ResultadoEnvio{MessageID: r.Messages[0].ID}, nil
}
