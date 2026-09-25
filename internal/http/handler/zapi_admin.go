package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/caixa"
	"github.com/LucasGardoni/whatsapp-gateway/internal/provedor/zapi"
)

// ZAPIAdmin expoe ao CRM as acoes de supervisor que exigem falar
// diretamente com a z-api (gestao de fila e qr code de reconexao, secao
// 4.7/4.9 -- fase 9). So faz sentido pra z-api, entao recebe o cliente
// concreto em vez da interface provedor.Provedor.
//
// Cada acao vale para a caixa do parametro `caixa` da query (G1); sem ele,
// a caixa padrao. Caixa que nao e z-api responde 409.
type ZAPIAdmin struct {
	caixas  caixasV1
	cliente func(caixa.Caixa) (*zapi.Cliente, bool)
}

func NovoZAPIAdmin(caixas *caixa.Registro) *ZAPIAdmin {
	return &ZAPIAdmin{caixas: caixas, cliente: caixas.ZAPI}
}

// clienteDaCaixa resolve a caixa da query e o cliente z-api dela. false ja
// respondeu.
func (h *ZAPIAdmin) clienteDaCaixa(w http.ResponseWriter, r *http.Request) (*zapi.Cliente, bool) {
	cx, ok := caixaDaRequisicao(w, r, h.caixas, r.URL.Query().Get("caixa"))
	if !ok {
		return nil, false
	}
	cl, ok := h.cliente(cx)
	if !ok {
		http.Error(w, "caixa "+cx.Codigo+" nao usa a z-api", http.StatusConflict)
		return nil, false
	}
	return cl, true
}

// Fila repassa o json bruto da z-api -- o formato dos itens e detalhe da
// z-api, o supervisor so precisa enxergar o que esta parado la.
func (h *ZAPIAdmin) Fila(w http.ResponseWriter, r *http.Request) {
	cliente, ok := h.clienteDaCaixa(w, r)
	if !ok {
		return
	}
	corpo, err := cliente.Fila(r.Context())
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
	cliente, ok := h.clienteDaCaixa(w, r)
	if !ok {
		return
	}
	if err := cliente.LimparFila(r.Context()); err != nil {
		slog.Error("zapi admin: limpar fila", "erro", err)
		http.Error(w, "erro ao limpar fila", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LimparItemFila descarta um unico item pelo id.
func (h *ZAPIAdmin) LimparItemFila(w http.ResponseWriter, r *http.Request) {
	cliente, ok := h.clienteDaCaixa(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := cliente.LimparItemFila(r.Context(), id); err != nil {
		slog.Error("zapi admin: limpar item da fila", "id", id, "erro", err)
		http.Error(w, "erro ao limpar item da fila", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// QRCode repassa a imagem do qr code de reconexao pro CRM exibir na tela
// do supervisor (secao 4.9).
func (h *ZAPIAdmin) QRCode(w http.ResponseWriter, r *http.Request) {
	cliente, ok := h.clienteDaCaixa(w, r)
	if !ok {
		return
	}
	resultado, err := cliente.QRCodeImagem(r.Context())
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

type respostaTokenChamada struct {
	Token      string `json:"token"`
	InstanceID string `json:"instance_id"`
}

// TokenChamada entrega a aplicacao o token efemero da SDK de chamadas da
// z-api. A aplicacao repassa ao browser depois de conferir quem pode
// ligar -- o gateway nao sabe de usuario nem de horario de departamento.
// POST porque cada chamada cria um token novo na z-api.
func (h *ZAPIAdmin) TokenChamada(w http.ResponseWriter, r *http.Request) {
	cliente, ok := h.clienteDaCaixa(w, r)
	if !ok {
		return
	}
	token, err := cliente.TokenChamada(r.Context())
	if err != nil {
		slog.Error("zapi admin: gerar token de chamada", "erro", err)
		http.Error(w, "erro ao gerar token de chamada", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// token de uso unico: cache devolveria um token ja consumido.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(respostaTokenChamada{Token: token, InstanceID: cliente.InstanceID()})
}
