package zapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Grupo e um item de GET /groups. A z-api manda archived/pinned/isMuted ora
// como bool, ora como string: so os campos que o gateway usa sao lidos.
type Grupo struct {
	Phone string `json:"phone"`
	Name  string `json:"name"`
}

// Grupos le uma pagina dos grupos da instancia. page comeca em 1; pagina alem
// da ultima vem `[]` ou `null`.
func (c *Cliente) Grupos(ctx context.Context, page, pageSize int) ([]Grupo, error) {
	req, err := c.novaRequisicao(ctx, http.MethodGet, fmt.Sprintf("groups?page=%d&pageSize=%d", page, pageSize), nil)
	if err != nil {
		return nil, fmt.Errorf("listar grupos: %w", err)
	}
	corpo, err := c.corpoOuErro(req, "listar grupos")
	if err != nil {
		return nil, err
	}
	var grupos []Grupo
	if err := json.Unmarshal(corpo, &grupos); err != nil {
		return nil, fmt.Errorf("listar grupos: resposta nao reconhecida: %s", corpo)
	}
	return grupos, nil
}

// ParticipanteGrupo e um membro do grupo no metadata.
type ParticipanteGrupo struct {
	Phone        string `json:"phone"`
	IsAdmin      bool   `json:"isAdmin"`
	IsSuperAdmin bool   `json:"isSuperAdmin"`
	Short        string `json:"short"`
	Name         string `json:"name"`
}

// MetadataGrupo e o retorno de light-group-metadata (sem o link de convite,
// que a doc diz ser custoso de obter).
type MetadataGrupo struct {
	Phone             string              `json:"phone"`
	Subject           string              `json:"subject"`
	Description       string              `json:"description"`
	Owner             string              `json:"owner"`
	Creation          int64               `json:"creation"`
	AdminOnlyMessage  bool                `json:"adminOnlyMessage"`
	AdminOnlySettings bool                `json:"adminOnlySettings"`
	CommunityID       *string             `json:"communityId"`
	Participants      []ParticipanteGrupo `json:"participants"`
}

func (c *Cliente) MetadataGrupo(ctx context.Context, grupoID string) (*MetadataGrupo, error) {
	req, err := c.novaRequisicao(ctx, http.MethodGet, "light-group-metadata/"+url.PathEscape(grupoID), nil)
	if err != nil {
		return nil, fmt.Errorf("metadata do grupo: %w", err)
	}
	corpo, err := c.corpoOuErro(req, "metadata do grupo")
	if err != nil {
		return nil, err
	}
	var m MetadataGrupo
	if err := json.Unmarshal(corpo, &m); err != nil {
		return nil, fmt.Errorf("metadata do grupo: resposta nao reconhecida: %s", corpo)
	}
	return &m, nil
}

type respostaSucesso struct {
	Success bool   `json:"success"`
	Value   bool   `json:"value"`
	Error   string `json:"error"`
}

// AceitarConvite entra no grupo pelo link (chat.whatsapp.com/...).
func (c *Cliente) AceitarConvite(ctx context.Context, link string) error {
	req, err := c.novaRequisicao(ctx, http.MethodGet, "accept-invite-group?"+url.Values{"url": {link}}.Encode(), nil)
	if err != nil {
		return fmt.Errorf("aceitar convite: %w", err)
	}
	corpo, err := c.corpoOuErro(req, "aceitar convite")
	if err != nil {
		return err
	}
	var r respostaSucesso
	if err := json.Unmarshal(corpo, &r); err != nil || r.Error != "" || !r.Success {
		return fmt.Errorf("aceitar convite: z-api nao confirmou: %s", corpo)
	}
	return nil
}

// SairDoGrupo tira o numero da instancia do grupo.
func (c *Cliente) SairDoGrupo(ctx context.Context, grupoID string) error {
	corpoJSON, _ := json.Marshal(map[string]string{"groupId": grupoID})
	req, err := c.novaRequisicao(ctx, http.MethodPost, "leave-group", bytes.NewReader(corpoJSON))
	if err != nil {
		return fmt.Errorf("sair do grupo: %w", err)
	}
	corpo, err := c.corpoOuErro(req, "sair do grupo")
	if err != nil {
		return err
	}
	var r respostaSucesso
	if err := json.Unmarshal(corpo, &r); err != nil || r.Error != "" || !(r.Value || r.Success) {
		return fmt.Errorf("sair do grupo: z-api nao confirmou: %s", corpo)
	}
	return nil
}
