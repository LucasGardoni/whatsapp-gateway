package midia

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mimePorExtensao e o inverso de extensoesConhecidas -- aqui a direcao e
// arquivo em disco para content-type, usado ao montar a data URI de envio.
//
// Esta lista tem de cobrir TODA extensao que o lado CRM sabe classificar
// (../CRMFrancoSuico/app/Libraries/Crm/TipoMidia.php). Quando divergiam, o
// CRM aceitava um .docx como "documento", o Go nao reconhecia a extensao e
// mandava application/octet-stream -- o WhatsApp recebia o arquivo sem
// saber o que era e o cliente via um anexo generico ou nada. As duas
// listas andam juntas: mexer numa exige mexer na outra.
var mimePorExtensao = map[string]string{
	// imagem
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	// audio
	".mp3": "audio/mpeg",
	".ogg": "audio/ogg",
	".oga": "audio/ogg",
	".m4a": "audio/mp4",
	".wav": "audio/wav",
	// video
	".mp4":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	// documento
	".pdf":  "application/pdf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// ErroForaDoDiretorio indica tentativa de ler arquivo fora de MIDIA_DIR.
// Erro proprio para o worker poder classificar como falha definitiva: nao
// adianta retentar um caminho que nunca vai ser permitido.
var ErroForaDoDiretorio = errors.New("caminho fora do diretorio de midia")

// ResolverDentroDe confina caminho a raiz e devolve o caminho absoluto
// seguro. Decisao D-3 da auditoria: midia_biblioteca.caminho passa a ser
// relativo a MIDIA_DIR.
//
// Sem isto (P2-18), midia_biblioteca.caminho e texto livre vindo do CRUD
// de Admin do CRM: qualquer um que escreva nessa tabela faz o gateway ler
// um arquivo arbitrario do disco e mandar por WhatsApp -- inclusive o
// proprio .env com as credenciais.
//
// O caminho relativo e resolvido dentro da raiz; um caminho absoluto so
// passa se ja estiver dentro dela. `..` e barra invertida sao neutralizados
// por filepath.Clean antes da comparacao. A checagem usa o separador no
// final da raiz para "/dados/midia-outro" nao passar por prefixo de
// "/dados/midia".
func ResolverDentroDe(raiz, caminho string) (string, error) {
	raizAbs, err := filepath.Abs(raiz)
	if err != nil {
		return "", fmt.Errorf("resolver diretorio de midia %s: %w", raiz, err)
	}

	alvo := filepath.Clean(caminho)
	if !filepath.IsAbs(alvo) {
		alvo = filepath.Join(raizAbs, alvo)
	}
	alvoAbs, err := filepath.Abs(alvo)
	if err != nil {
		return "", fmt.Errorf("resolver caminho de midia %s: %w", caminho, err)
	}

	rel, err := filepath.Rel(raizAbs, alvoAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErroForaDoDiretorio, caminho)
	}

	return alvoAbs, nil
}

// CodificarBase64 le um arquivo de dentro de raiz (MIDIA_DIR) e devolve
// como data URI (data:<mime>;base64,<conteudo>) -- formato que a z-api
// aceita nos endpoints send-image/send-audio/send-video/send-document
// (secao 4.9). A z-api nao guarda midia a longo prazo, entao quem envia
// manda o conteudo direto, sem depender de URL publica.
func CodificarBase64(raiz, caminho string) (string, error) {
	seguro, err := ResolverDentroDe(raiz, caminho)
	if err != nil {
		return "", err
	}

	dados, err := os.ReadFile(seguro)
	if err != nil {
		return "", fmt.Errorf("codificar midia %s: %w", caminho, err)
	}

	mime := mimePorExtensao[strings.ToLower(filepath.Ext(caminho))]
	if mime == "" {
		mime = "application/octet-stream"
	}

	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(dados)), nil
}
