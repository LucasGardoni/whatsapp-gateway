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

// PayloadDesconexao e o corpo do webhook on-whatsapp-disconnected --
// alimenta provedor_saude (monitor de saude, fase 9).
type PayloadDesconexao struct {
	Disconnected bool   `json:"disconnected"`
	Error        string `json:"error"`
	InstanceID   string `json:"instanceId"`
}
