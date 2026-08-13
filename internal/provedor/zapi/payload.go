package zapi

// PayloadRecebido e o corpo do webhook on-message-received. Todo campo
// nao suportado ainda deve ser recuperado do payload_bruto persistido --
// nunca descarte o JSON original antes do parse (secao 10 do plano).
type PayloadRecebido struct {
	ChatLid        string `json:"chatLid"`
	SenderLid      string `json:"senderLid"`
	Phone          string `json:"phone"`
	ConnectedPhone string `json:"connectedPhone"`
	MessageID      string `json:"messageId"`
	Momment        int64  `json:"momment"`
	FromMe         bool   `json:"fromMe"`
	FromApi        bool   `json:"fromApi"`
	IsGroup        bool   `json:"isGroup"`
	IsNewsletter   bool   `json:"isNewsletter"`
	IsStatusReply  bool   `json:"isStatusReply"`
	SenderName     string `json:"senderName"`
	ChatName       string `json:"chatName"`
	Type           string `json:"type"`

	Text     *ConteudoTexto     `json:"text,omitempty"`
	Image    *ConteudoImagem    `json:"image,omitempty"`
	Audio    *ConteudoAudio     `json:"audio,omitempty"`
	Video    *ConteudoVideo     `json:"video,omitempty"`
	Document *ConteudoDocumento `json:"document,omitempty"`

	// ExternalAdReply so vem preenchido quando a conversa nasce de um
	// anuncio click-to-whatsapp (secao 4.5) -- atribuicao de campanha de
	// graca, persistida no lead mesmo sem uso imediato (fase 11).
	ExternalAdReply *ExternalAdReply `json:"externalAdReply,omitempty"`
}

type ExternalAdReply struct {
	SourceID string `json:"sourceId"`
	CtwaClid string `json:"ctwaClid"`
}

type ConteudoTexto struct {
	Message string `json:"message"`
}

type ConteudoImagem struct {
	URL           string `json:"imageUrl"`
	Caption       string `json:"caption"`
	MimeType      string `json:"mimeType"`
	DownloadError string `json:"downloadError"`
}

type ConteudoAudio struct {
	URL      string `json:"audioUrl"`
	MimeType string `json:"mimeType"`
}

type ConteudoVideo struct {
	URL      string `json:"videoUrl"`
	Caption  string `json:"caption"`
	MimeType string `json:"mimeType"`
}

type ConteudoDocumento struct {
	URL      string `json:"documentUrl"`
	MimeType string `json:"mimeType"`
	FileName string `json:"fileName"`
}

// PayloadStatusMensagem e o corpo do webhook on-message-status. IDs vem em
// lista -- um unico callback pode atualizar varias mensagens de uma vez.
type PayloadStatusMensagem struct {
	IDs    []string `json:"ids"`
	Status string   `json:"status"`
	Phone  string   `json:"phone"`
}

// PayloadEnvio e o corpo do webhook on-message-send (DeliveryCallback) --
// o resultado ASSINCRONO de um envio (P1-09).
//
// Ele existe porque a z-api aceita o send-text com 200 e so depois reporta
// que a mensagem nao saiu. Sem esta rota, a deteccao de shadowban dependia
// de a z-api falhar de forma sincrona na resposta do send-text; o caso do
// erro chegando depois passava em branco e a mensagem ficava marcada
// 'enviada' para sempre -- justamente o cenario que custa a reputacao do
// numero B, que e o ativo mais fragil do projeto.
type PayloadEnvio struct {
	// IDs plural para casar com on-message-status: um callback pode
	// carregar varias mensagens. `id` singular aparece em alguns payloads,
	// entao os dois sao aceitos -- ver IDsDeMensagem.
	IDs    []string `json:"ids"`
	ID     string   `json:"id"`
	ZaapID string   `json:"zaapId"`
	// Error vazio significa envio confirmado.
	Error  string `json:"error"`
	Status string `json:"status"`
	Phone  string `json:"phone"`
}

// IDsDeMensagem normaliza as duas formas em que a z-api identifica a
// mensagem no callback de envio. Sem isto, a forma singular seria ignorada
// em silencio e o erro nao chegaria a mensagem nenhuma.
func (p PayloadEnvio) IDsDeMensagem() []string {
	if len(p.IDs) > 0 {
		return p.IDs
	}
	if p.ID != "" {
		return []string{p.ID}
	}
	if p.ZaapID != "" {
		return []string{p.ZaapID}
	}
	return nil
}

// PayloadDesconexao e o corpo do webhook on-whatsapp-disconnected --
// alimenta provedor_saude (monitor de saude, fase 9).
type PayloadDesconexao struct {
	Disconnected bool   `json:"disconnected"`
	Error        string `json:"error"`
	InstanceID   string `json:"instanceId"`
}
