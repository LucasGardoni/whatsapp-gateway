package zapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type respostaFotoPerfil struct {
	Link string `json:"link"`
}

// FotoPerfil devolve o link da foto de perfil do contato ou grupo. Vazio =
// sem foto (ou escondida pela privacidade do contato). O link e da CDN do
// WhatsApp e expira: quem guarda tem de pedir de novo de tempos em tempos.
// A doc mostra o retorno ora como objeto, ora como lista de um item.
func (c *Cliente) FotoPerfil(ctx context.Context, phone string) (string, error) {
	req, err := c.novaRequisicao(ctx, http.MethodGet, "profile-picture?"+url.Values{"phone": {phone}}.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("foto de perfil: %w", err)
	}
	corpo, err := c.corpoOuErro(req, "foto de perfil")
	if err != nil {
		return "", err
	}

	var unica respostaFotoPerfil
	if err := json.Unmarshal(corpo, &unica); err == nil {
		return linkDeFoto(unica.Link), nil
	}
	var lista []respostaFotoPerfil
	if err := json.Unmarshal(corpo, &lista); err == nil {
		if len(lista) == 0 {
			return "", nil
		}
		return linkDeFoto(lista[0].Link), nil
	}
	return "", fmt.Errorf("foto de perfil: resposta nao reconhecida: %s", corpo)
}

// linkDeFoto descarta o que nao e URL: sem foto a z-api ja devolveu "null" em texto.
func linkDeFoto(link string) string {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, "https://") {
		return ""
	}
	return link
}
