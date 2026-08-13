package zapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor"
)

const baseURLZAPI = "https://api.z-api.io"

// timeoutRequisicao limita cada chamada a z-api. Generoso porque envio de
// midia manda o arquivo inteiro em base64 no corpo (secao 4.9), mas finito
// -- o objetivo e nao existir caminho onde o worker fique preso.
const timeoutRequisicao = 60 * time.Second

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
		// http.DefaultClient nao tem timeout: uma conexao que a z-api
		// aceita e nunca responde penduraria o ciclo do outbox para
		// sempre, e com ele toda a fila de saida.
		http: &http.Client{Timeout: timeoutRequisicao},
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
// O metodo e GET. Era POST, e a z-api responde 415 (Unsupported Media Type)
// -- entao a tela de Supervisao sempre mostrou "Gateway indisponivel" ao
// pedir a fila, e o log do CRM acumulava `HTTP 502 para /api/zapi/fila`.
// Confirmado chamando a z-api em 2026-08-13: GET devolve 200 com a lista
// (vazia: `[]`), POST devolve 415, DELETE 204.
func (c *Cliente) Fila(ctx context.Context) ([]byte, error) {
	req, err := c.novaRequisicao(ctx, http.MethodGet, "queue", nil)
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

// ResultadoQRCode e o qr code de reconexao ja interpretado.
//
// Conectada=true significa que NAO ha qr code para mostrar, e isso e um
// estado normal, nao erro: a instancia so gera qr quando esta desconectada.
type ResultadoQRCode struct {
	Conectada   bool
	ImagemPNG   []byte
	ContentType string
}

// respostaQRCode cobre as formas que a z-api devolve em /qr-code/image.
// O nome do campo do base64 nao esta fixado na doc, entao aceitamos os
// candidatos conhecidos em vez de apostar num -- errar o nome devolveria
// "sem qr" para sempre, sem erro nenhum.
type respostaQRCode struct {
	Connected bool   `json:"connected"`
	Value     string `json:"value"`
	QRCode    string `json:"qrcode"`
	Image     string `json:"image"`
	Base64    string `json:"base64"`
	Error     string `json:"error"`
}

func (r respostaQRCode) base64Encontrado() string {
	for _, v := range []string{r.Value, r.QRCode, r.Image, r.Base64} {
		if v != "" {
			return v
		}
	}
	return ""
}

// QRCodeImagem devolve o qr code de reconexao (tela do supervisor, secao
// 4.9), decodificado em PNG.
//
// P1-06, confirmado em 2026-08-13 chamando a z-api de verdade -- eram DOIS
// bugs, e o efeito combinado era o recurso nunca ter funcionado:
//
//  1. O path era "qr-code-image", que a z-api responde com
//     {"error":"NOT_FOUND"}. O correto e "qr-code/image".
//  2. A resposta e application/json, nao imagem binaria. O codigo antigo
//     repassava o corpo cru com o Content-Type da z-api, entao o <img> do
//     CRM recebia JSON marcado como JSON, mostrava imagem quebrada, e o
//     onerror da tela escondia o elemento -- falha silenciosa completa.
func (c *Cliente) QRCodeImagem(ctx context.Context) (*ResultadoQRCode, error) {
	req, err := c.novaRequisicao(ctx, http.MethodGet, "qr-code/image", nil)
	if err != nil {
		return nil, fmt.Errorf("obter qr code: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("obter qr code: %w", err)
	}
	defer resp.Body.Close()

	corpo, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("obter qr code: ler resposta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("obter qr code: status %d da z-api: %s", resp.StatusCode, corpo)
	}

	// se um dia a z-api voltar a mandar imagem binaria, repassa direto.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/") {
		return &ResultadoQRCode{ImagemPNG: corpo, ContentType: ct}, nil
	}

	var r respostaQRCode
	if err := json.Unmarshal(corpo, &r); err != nil {
		return nil, fmt.Errorf("obter qr code: resposta nao reconhecida (%s): %s", resp.Header.Get("Content-Type"), corpo)
	}
	if r.Error != "" {
		return nil, fmt.Errorf("obter qr code: z-api respondeu erro: %s", r.Error)
	}
	if r.Connected {
		return &ResultadoQRCode{Conectada: true}, nil
	}

	bruto := r.base64Encontrado()
	if bruto == "" {
		return nil, fmt.Errorf("obter qr code: resposta sem imagem e sem connected: %s", corpo)
	}

	// a z-api pode mandar o base64 puro ou ja como data URI.
	if i := strings.Index(bruto, ","); strings.HasPrefix(bruto, "data:") && i > 0 {
		bruto = bruto[i+1:]
	}

	imagem, err := base64.StdEncoding.DecodeString(bruto)
	if err != nil {
		return nil, fmt.Errorf("obter qr code: decodificar base64: %w", err)
	}

	return &ResultadoQRCode{ImagemPNG: imagem, ContentType: "image/png"}, nil
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
