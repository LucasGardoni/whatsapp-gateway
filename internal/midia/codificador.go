package midia

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mimePorExtensao e o inverso de extensoesConhecidas -- aqui a direcao e
// arquivo em disco para content-type, usado ao montar a data URI de envio.
var mimePorExtensao = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".ogg":  "audio/ogg",
	".mp3":  "audio/mpeg",
	".mp4":  "video/mp4",
	".pdf":  "application/pdf",
}

// CodificarBase64 le um arquivo do disco e devolve como data URI
// (data:<mime>;base64,<conteudo>) -- formato que a z-api aceita nos
// endpoints send-image/send-audio/send-video/send-document (secao 4.9).
// A z-api nao guarda midia a longo prazo, entao quem envia manda o
// conteudo direto, sem depender de URL publica.
func CodificarBase64(caminho string) (string, error) {
	dados, err := os.ReadFile(caminho)
	if err != nil {
		return "", fmt.Errorf("codificar midia %s: %w", caminho, err)
	}

	mime := mimePorExtensao[strings.ToLower(filepath.Ext(caminho))]
	if mime == "" {
		mime = "application/octet-stream"
	}

	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(dados)), nil
}
