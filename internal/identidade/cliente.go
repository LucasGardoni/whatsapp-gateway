package identidade

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const baseURLZAPI = "https://api.z-api.io"

// Cliente resolve telefone em @lid via /contacts/get-iswhatsapp da Z-API.
// Deve ser usado apenas no momento do disparo (numero A), quando o telefone
// do lead ainda e conhecido -- ver secao 4.3 do plano.
type Cliente struct {
	baseURL       string
	instanceID    string
	instanceToken string
	clientToken   string
	http          *http.Client
}

// NovoCliente cria o cliente com as credenciais vindas de config (variavel
// de ambiente). Nunca receba essas credenciais de outra fonte.
func NovoCliente(instanceID, instanceToken, clientToken string) *Cliente {
	return novoClienteComBase(baseURLZAPI, instanceID, instanceToken, clientToken)
}

// NovoClienteComBase e NovoCliente com host configuravel -- usado por
// testes de integracao de outros pacotes que compoe Cliente (ex.:
// handler.Disparo) contra um servidor fake em vez da z-api real.
func NovoClienteComBase(baseURL, instanceID, instanceToken, clientToken string) *Cliente {
	return novoClienteComBase(baseURL, instanceID, instanceToken, clientToken)
}

func novoClienteComBase(baseURL, instanceID, instanceToken, clientToken string) *Cliente {
	return &Cliente{
		baseURL:       baseURL,
		instanceID:    instanceID,
		instanceToken: instanceToken,
		clientToken:   clientToken,
		http:          http.DefaultClient,
	}
}

// ResultadoLid e a resposta de get-iswhatsapp: se o telefone existe no
// WhatsApp e, em caso positivo, o @lid correspondente.
type ResultadoLid struct {
	Existe   bool
	Telefone string
	Lid      string
}

type respostaGetIsWhatsapp struct {
	Exists bool   `json:"exists"`
	Phone  string `json:"phone"`
	Lid    string `json:"lid"`
}

// ResolverLid consulta a Z-API e devolve o @lid associado ao telefone.
// Chame com o telefone ja normalizado em E.164 sem o "+".
func (c *Cliente) ResolverLid(ctx context.Context, telefone string) (*ResultadoLid, error) {
	url := fmt.Sprintf("%s/instances/%s/token/%s/contacts/get-iswhatsapp?phone=%s",
		c.baseURL, c.instanceID, c.instanceToken, telefone)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("resolver lid %s: %w", telefone, err)
	}
	if c.clientToken != "" {
		req.Header.Set("Client-Token", c.clientToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolver lid %s: %w", telefone, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resolver lid %s: status %d da z-api", telefone, resp.StatusCode)
	}

	var respostas []respostaGetIsWhatsapp
	if err := json.NewDecoder(resp.Body).Decode(&respostas); err != nil {
		return nil, fmt.Errorf("resolver lid %s: decodificar resposta: %w", telefone, err)
	}
	if len(respostas) == 0 {
		return nil, fmt.Errorf("resolver lid %s: resposta vazia da z-api", telefone)
	}

	r := respostas[0]
	return &ResultadoLid{
		Existe:   r.Exists,
		Telefone: r.Phone,
		Lid:      r.Lid,
	}, nil
}
