// package midia baixa anexos do webhook para disco -- a z-api guarda por
// 30 dias, mas a persistencia de longo prazo e responsabilidade nossa
// (secao 4.6 do plano).
package midia

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Baixador struct {
	diretorio string
	http      *http.Client
}

// timeoutDownload limita o download de midia recebida. Roda dentro do
// processamento do webhook, entao sem limite um servidor de midia lento
// segura o goroutine indefinidamente -- http.DefaultClient nao tem timeout.
const timeoutDownload = 60 * time.Second

func NovoBaixador(diretorio string) *Baixador {
	return &Baixador{diretorio: diretorio, http: &http.Client{Timeout: timeoutDownload}}
}

// extensoesConhecidas evita depender do banco de mime types do SO (o
// pacote mime le o registro do Windows, que varia por maquina) para os
// tipos que a v1 precisa suportar (secao 4.5).
var extensoesConhecidas = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"audio/ogg":       ".ogg",
	"audio/mpeg":      ".mp3",
	"video/mp4":       ".mp4",
	"application/pdf": ".pdf",
}

func extensao(contentType, urlBruta string) string {
	mediaType := contentType
	if i := strings.Index(mediaType, ";"); i >= 0 {
		mediaType = mediaType[:i]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if ext, ok := extensoesConhecidas[mediaType]; ok {
		return ext
	}
	// Demais tipos que o envio conhece (docx, xlsx, m4a...): sem isto o
	// anexo recebido virava .bin e ninguem abria.
	for ext, mime := range mimePorExtensao {
		if mime == mediaType {
			return ext
		}
	}
	if u, err := url.Parse(urlBruta); err == nil {
		if ext := filepath.Ext(u.Path); ext != "" {
			return ext
		}
	}
	return ".bin"
}

// Baixar busca a URL do anexo e grava em disco com nome baseado em
// nomeBase (usar o messageId do provedor -- ja e unico por mensagem).
// Devolve o caminho completo gravado em mensagem.midia_caminho.
func (b *Baixador) Baixar(ctx context.Context, urlBruta, nomeBase string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlBruta, nil)
	if err != nil {
		return "", fmt.Errorf("baixar midia %s: %w", nomeBase, err)
	}

	resp, err := b.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("baixar midia %s: %w", nomeBase, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("baixar midia %s: %w", nomeBase, ErroStatus{Status: resp.StatusCode})
	}

	if err := os.MkdirAll(b.diretorio, 0o755); err != nil {
		return "", fmt.Errorf("baixar midia %s: criar diretorio: %w", nomeBase, err)
	}

	caminho := filepath.Join(b.diretorio, nomeBase+extensao(resp.Header.Get("Content-Type"), urlBruta))

	arquivo, err := os.Create(caminho)
	if err != nil {
		return "", fmt.Errorf("baixar midia %s: criar arquivo: %w", nomeBase, err)
	}

	if _, err := io.Copy(arquivo, resp.Body); err != nil {
		// arquivo pela metade nao pode ficar no disco: a proxima tentativa
		// grava por cima, mas se ela nunca vier a tela serviria a imagem cortada.
		arquivo.Close()
		_ = os.Remove(caminho)
		return "", fmt.Errorf("baixar midia %s: gravar arquivo: %w", nomeBase, err)
	}
	if err := arquivo.Close(); err != nil {
		_ = os.Remove(caminho)
		return "", fmt.Errorf("baixar midia %s: fechar arquivo: %w", nomeBase, err)
	}

	return caminho, nil
}

// ErroStatus e a resposta nao-200 do servidor de midia.
type ErroStatus struct{ Status int }

func (e ErroStatus) Error() string { return fmt.Sprintf("status %d", e.Status) }

// Definitivo diz que tentar de novo nao adianta: o link temporario da z-api
// expirou ou nunca existiu. 408/429 e 5xx sao passageiros.
func (e ErroStatus) Definitivo() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusRequestTimeout && e.Status != http.StatusTooManyRequests
}
