package zapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

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

// NovoClienteComBase e NovoCliente com host configuravel -- usado por
// testes de outros pacotes que compoe Cliente (ex.: handler.ZAPIAdmin)
// contra um servidor fake em vez da z-api real (mesmo padrao de
// internal/identidade.NovoClienteComBase).
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
	return c.enviar(ctx, "send-text", map[string]any{
		"phone":        msg.Destinatario,
		"message":      msg.Texto,
		"delayMessage": 3,
		"delayTyping":  delayTyping(msg.Texto),
	})
}

// camposEnvioMidia mapeia o tipo de mensagem.tipo para o endpoint da z-api
// e o nome do campo que carrega o conteudo (secao 4.9). Documento e o unico
// que leva a extensao no path -- a z-api usa isso pra decidir o icone/preview.
func camposEnvioMidia(msg provedor.MensagemMidia) (caminho, campo string, err error) {
	switch msg.Tipo {
	case "imagem":
		return "send-image", "image", nil
	case "audio":
		return "send-audio", "audio", nil
	case "video":
		return "send-video", "video", nil
	case "documento":
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(msg.NomeArquivo)), ".")
		if ext == "" {
			ext = "pdf"
		}
		return "send-document/" + ext, "document", nil
	default:
		return "", "", fmt.Errorf("tipo de midia nao suportado pela z-api: %q", msg.Tipo)
	}
}

// EnviarMidia manda imagem/audio/video/documento -- so implementado a
// partir da fase 9 (antes disso o outbox so mandava texto).
func (c *Cliente) EnviarMidia(ctx context.Context, msg provedor.MensagemMidia) (*provedor.ResultadoEnvio, error) {
	caminho, campo, err := camposEnvioMidia(msg)
	if err != nil {
		return nil, fmt.Errorf("enviar midia: %w", &ErroEnvio{Tipo: ErroNaoRetentavel, Mensagem: err.Error()})
	}

	corpo := map[string]any{
		"phone": msg.Destinatario,
		campo:   msg.ConteudoBase64,
	}
	if msg.Legenda != "" {
		corpo["caption"] = msg.Legenda
	}
	if msg.Tipo == "documento" && msg.NomeArquivo != "" {
		corpo["fileName"] = msg.NomeArquivo
	}

	return c.enviar(ctx, caminho, corpo)
}

// enviar concentra o padrao comum a todo endpoint de envio da z-api:
// monta o corpo, faz o post e classifica o erro (retentavel/shadowban).
func (c *Cliente) enviar(ctx context.Context, caminho string, corpo map[string]any) (*provedor.ResultadoEnvio, error) {
	corpoJSON, err := json.Marshal(corpo)
	if err != nil {
		return nil, fmt.Errorf("enviar mensagem: %w", err)
	}

	req, err := c.novaRequisicao(ctx, http.MethodPost, caminho, bytes.NewReader(corpoJSON))
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

// Fila lista as mensagens acumuladas na fila propria da z-api (secao 4.7).
// Devolve o JSON bruto -- o formato exato dos itens nao importa pro
// gateway, so o supervisor via CRM precisa enxergar o que esta parado la.
func (c *Cliente) Fila(ctx context.Context) ([]byte, error) {
	req, err := c.novaRequisicao(ctx, http.MethodPost, "queue", nil)
	if err != nil {
		return nil, fmt.Errorf("consultar fila: %w", err)
	}
	return c.corpoOuErro(req, "consultar fila")
}

// LimparFila descarta toda a fila da instancia -- usado quando reconecta
// com mensagens antigas paradas la que ja nao fazem mais sentido (secao 4.7).
func (c *Cliente) LimparFila(ctx context.Context) error {
	req, err := c.novaRequisicao(ctx, http.MethodDelete, "queue", nil)
	if err != nil {
		return fmt.Errorf("limpar fila: %w", err)
	}
	_, err = c.corpoOuErro(req, "limpar fila")
	return err
}

// LimparItemFila descarta um unico item da fila pelo id.
func (c *Cliente) LimparItemFila(ctx context.Context, id string) error {
	req, err := c.novaRequisicao(ctx, http.MethodDelete, "queue/"+id, nil)
	if err != nil {
		return fmt.Errorf("limpar item da fila %s: %w", id, err)
	}
	_, err = c.corpoOuErro(req, "limpar item da fila "+id)
	return err
}

// QRCodeImagem devolve a imagem do qr code para reconectar a instancia
// (tela do supervisor, secao 4.9) junto do content-type devolvido pela
// z-api, pra repassar sem reinterpretar o formato.
func (c *Cliente) QRCodeImagem(ctx context.Context) (imagem []byte, contentType string, err error) {
	req, err := c.novaRequisicao(ctx, http.MethodGet, "qr-code-image", nil)
	if err != nil {
		return nil, "", fmt.Errorf("obter qr code: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("obter qr code: %w", err)
	}
	defer resp.Body.Close()

	corpo, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("obter qr code: ler resposta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("obter qr code: status %d da z-api: %s", resp.StatusCode, corpo)
	}

	return corpo, resp.Header.Get("Content-Type"), nil
}

// corpoOuErro roda uma requisicao ja montada e devolve o corpo quando o
// status e 200, ou um erro com o corpo da resposta caso contrario.
func (c *Cliente) corpoOuErro(req *http.Request, contexto string) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", contexto, err)
	}
	defer resp.Body.Close()

	corpo, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: ler resposta: %w", contexto, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d da z-api: %s", contexto, resp.StatusCode, corpo)
	}
	return corpo, nil
}
