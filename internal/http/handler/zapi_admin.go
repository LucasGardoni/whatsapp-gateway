package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
)

// ZAPIAdmin expoe ao CRM as acoes de supervisor que exigem falar
// diretamente com a z-api (gestao de fila e qr code de reconexao, secao
// 4.7/4.9 -- fase 9). So faz sentido pra z-api, entao recebe o cliente
// concreto em vez da interface provedor.Provedor.
type ZAPIAdmin struct {
	cliente *zapi.Cliente
}

func NovoZAPIAdmin(cliente *zapi.Cliente) *ZAPIAdmin {
	return &ZAPIAdmin{cliente: cliente}
}

// Fila repassa o json bruto da z-api -- o formato dos itens e detalhe da
// z-api, o supervisor so precisa enxergar o que esta parado la.
func (h *ZAPIAdmin) Fila(w http.ResponseWriter, r *http.Request) {
	corpo, err := h.cliente.Fila(r.Context())
	if err != nil {
		slog.Error("zapi admin: consultar fila", "erro", err)
		http.Error(w, "erro ao consultar fila", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(corpo)
}

// LimparFila descarta toda a fila -- usado na reconexao quando ha
// mensagens antigas paradas la que ja nao fazem mais sentido (secao 4.7).
func (h *ZAPIAdmin) LimparFila(w http.ResponseWriter, r *http.Request) {
	if err := h.cliente.LimparFila(r.Context()); err != nil {
		slog.Error("zapi admin: limpar fila", "erro", err)
		http.Error(w, "erro ao limpar fila", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LimparItemFila descarta um unico item pelo id.
func (h *ZAPIAdmin) LimparItemFila(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.cliente.LimparItemFila(r.Context(), id); err != nil {
		slog.Error("zapi admin: limpar item da fila", "id", id, "erro", err)
		http.Error(w, "erro ao limpar item da fila", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// QRCode repassa a imagem do qr code de reconexao pro CRM exibir na tela
// do supervisor (secao 4.9).
func (h *ZAPIAdmin) QRCode(w http.ResponseWriter, r *http.Request) {
	resultado, err := h.cliente.QRCodeImagem(r.Context())
	if err != nil {
		slog.Error("zapi admin: obter qr code", "erro", err)
		http.Error(w, "erro ao obter qr code", http.StatusBadGateway)
		return
	}

	// instancia conectada nao tem qr code, e isso nao e erro -- e a
	// situacao normal. 409 distingue do 502 de falha real, para a tela
	// poder dizer "conectada, nao precisa de qr" em vez de esconder o
	// elemento e deixar o supervisor sem saber o que aconteceu (P1-06).
	if resultado.Conectada {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"conectada":true,"detalhe":"instancia ja conectada, qr code nao se aplica"}`))
		return
	}

	w.Header().Set("Content-Type", resultado.ContentType)
	// o qr do WhatsApp expira em segundos -- cache aqui serviria imagem
	// morta e o supervisor tentaria escanear um codigo invalido.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(resultado.ImagemPNG)
}
