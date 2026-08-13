package identidade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
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
		// resolucao de @lid roda dentro do POST /disparos, que e sincrono
		// para quem chamou -- sem timeout, a requisicao do CRM ficaria
		// pendurada junto. Mais curto que os de envio: e so um lookup.
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// ResultadoLid e a resposta de get-iswhatsapp: se o telefone existe no
// WhatsApp e, em caso positivo, o @lid correspondente.
type ResultadoLid struct {
	// Resolvido diz se a z-api respondeu algo sobre este telefone. false
	// significa ausencia de informacao (resposta `null` ou vazia), nao
	// "telefone inexistente" -- ver decodificarGetIsWhatsapp.
	Resolvido bool
	Existe    bool
	Telefone  string
	Lid       string
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

	corpo, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("resolver lid %s: ler resposta: %w", telefone, err)
	}

	r, resolvido := decodificarGetIsWhatsapp(corpo)
	return &ResultadoLid{
		Resolvido: resolvido,
		Existe:    r.Exists,
		Telefone:  r.Phone,
		Lid:       r.Lid,
	}, nil
}

// decodificarGetIsWhatsapp aceita as tres formas que /contacts/get-iswhatsapp
// devolve na pratica: array (documentado), objeto unico, e `null` puro com
// status 200 -- observado em 2026-08-13 num telefone que a z-api simplesmente
// nao resolveu.
//
// resolvido=false significa "a z-api nao respondeu nada sobre este telefone",
// que e diferente de exists=false ("respondeu que nao esta no WhatsApp"). A
// distincao importa: o primeiro caso nao e informacao nenhuma e nao deve
// impedir o disparo; o segundo e um dado de negocio.
func decodificarGetIsWhatsapp(corpo []byte) (respostaGetIsWhatsapp, bool) {
	var lista []respostaGetIsWhatsapp
	if err := json.Unmarshal(corpo, &lista); err == nil {
		if len(lista) == 0 {
			return respostaGetIsWhatsapp{}, false
		}
		return lista[0], true
	}

	var unico respostaGetIsWhatsapp
	if err := json.Unmarshal(corpo, &unico); err == nil {
		return unico, true
	}

	return respostaGetIsWhatsapp{}, false
}
