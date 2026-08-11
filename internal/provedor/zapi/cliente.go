package zapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
)

const baseURLZAPI = "https://api.z-api.io"

// Cliente implementa provedor.Provedor para o numero B. Credenciais vem
// sempre de config (variavel de ambiente) -- nunca em codigo.
type Cliente struct {
	baseURL       string
	instanceID    string
	instanceToken string
	clientToken   string
	http          *http.Client
}

var _ provedor.Provedor = (*Cliente)(nil)

func NovoCliente(instanceID, instanceToken, clientToken string) *Cliente {
	return novoClienteComBase(baseURLZAPI, instanceID, instanceToken, clientToken)
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

func (c *Cliente) urlInstancia(caminho string) string {
	return fmt.Sprintf("%s/instances/%s/token/%s/%s", c.baseURL, c.instanceID, c.instanceToken, caminho)
}

func (c *Cliente) novaRequisicao(ctx context.Context, metodo, caminho string, corpo io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, metodo, c.urlInstancia(caminho), corpo)
	if err != nil {
		return nil, err
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.clientToken != "" {
		req.Header.Set("Client-Token", c.clientToken)
	}
	return req, nil
}

type respostaEnviar struct {
	ZaapID    string `json:"zaapId"`
	MessageID string `json:"messageId"`
	ID        string `json:"id"`
	Error     string `json:"error"`
}

// Enviar manda texto com delayMessage e delayTyping proporcionais ao
// tamanho da mensagem -- nao sao cosmeticos, mitigam banimento (secao 4.2).
func (c *Cliente) Enviar(ctx context.Context, msg provedor.MensagemTexto) (*provedor.ResultadoEnvio, error) {
	corpo, err := json.Marshal(map[string]any{
		"phone":        msg.Destinatario,
		"message":      msg.Texto,
		"delayMessage": 3,
		"delayTyping":  delayTyping(msg.Texto),
	})
	if err != nil {
		return nil, fmt.Errorf("enviar mensagem: %w", err)
	}

	req, err := c.novaRequisicao(ctx, http.MethodPost, "send-text", bytes.NewReader(corpo))
	if err != nil {
		return nil, fmt.Errorf("enviar mensagem: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enviar mensagem: %w", &ErroEnvio{Tipo: ErroRetentavel, Mensagem: err.Error()})
	}
	defer resp.Body.Close()

	corpoResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("enviar mensagem: ler resposta: %w", err)
	}

	var r respostaEnviar
	_ = json.Unmarshal(corpoResp, &r)

	if resp.StatusCode != http.StatusOK || ehShadowban(r.Error) {
		tipo := ClassificarErro(resp.StatusCode, r.Error+" "+string(corpoResp))
		return nil, fmt.Errorf("enviar mensagem: %w", &ErroEnvio{
			Tipo:       tipo,
			StatusHTTP: resp.StatusCode,
			Mensagem:   string(corpoResp),
		})
	}

	return &provedor.ResultadoEnvio{
		ZaapID:    r.ZaapID,
		MessageID: r.MessageID,
		ID:        r.ID,
	}, nil
}

// delayTyping simula o tempo de digitacao proporcional ao texto, entre 1 e
// 15 segundos (secao 4.2 -- parte da mitigacao de banimento).
func delayTyping(texto string) int {
	const caracteresPorSegundo = 12
	d := len(texto) / caracteresPorSegundo
	if d < 1 {
		d = 1
	}
	if d > 15 {
		d = 15
	}
	return d
}

type respostaStatus struct {
	Connected           bool   `json:"connected"`
	SmartphoneConnected bool   `json:"smartphoneConnected"`
	Error               string `json:"error"`
}

// Status consulta /status -- monitor de saude da instancia.
func (c *Cliente) Status(ctx context.Context) (*provedor.StatusInstancia, error) {
	req, err := c.novaRequisicao(ctx, http.MethodGet, "status", nil)
	if err != nil {
		return nil, fmt.Errorf("consultar status: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("consultar status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		corpo, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("consultar status: status %d da z-api: %s", resp.StatusCode, corpo)
	}

	var r respostaStatus
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("consultar status: decodificar resposta: %w", err)
	}

	return &provedor.StatusInstancia{
		Conectada:           r.Connected,
		SmartphoneConectado: r.SmartphoneConnected,
		Detalhe:             r.Error,
	}, nil
}
