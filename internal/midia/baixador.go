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
)

type Baixador struct {
	diretorio string
	http      *http.Client
}

func NovoBaixador(diretorio string) *Baixador {
	return &Baixador{diretorio: diretorio, http: http.DefaultClient}
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
		return "", fmt.Errorf("baixar midia %s: status %d", nomeBase, resp.StatusCode)
	}

	if err := os.MkdirAll(b.diretorio, 0o755); err != nil {
		return "", fmt.Errorf("baixar midia %s: criar diretorio: %w", nomeBase, err)
	}

	caminho := filepath.Join(b.diretorio, nomeBase+extensao(resp.Header.Get("Content-Type"), urlBruta))

	arquivo, err := os.Create(caminho)
	if err != nil {
		return "", fmt.Errorf("baixar midia %s: criar arquivo: %w", nomeBase, err)
	}
	defer arquivo.Close()

	if _, err := io.Copy(arquivo, resp.Body); err != nil {
		return "", fmt.Errorf("baixar midia %s: gravar arquivo: %w", nomeBase, err)
	}

	return caminho, nil
}
