package handler

import (
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

const (
	limitePadraoBusca   = 200
	limiteMaximoBusca   = 500
	tamanhoMinimoBusca  = 3
	folgaTrechoBusca    = 25
	tamanhoTrechoMaximo = 160
)

// semAcento espelha o translate(lower()) de BuscarTextoNasConversas: mesma
// letra dos dois lados, para o trecho cair onde o banco achou.
var semAcento = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c",
)

func normalizarBusca(s string) string {
	return semAcento.Replace(strings.ToLower(s))
}

// escaparLike tira o efeito de % _ \ digitados: o termo e literal.
func escaparLike(s string) string {
	return strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type resultadoBuscaResponse struct {
	ConversaID int64  `json:"conversa_id"`
	MensagemID int64  `json:"mensagem_id"`
	Direcao    string `json:"direcao"`
	Tipo       string `json:"tipo"`
	Trecho     string `json:"trecho"`
	CriadoEm   string `json:"criado_em"`
}

type buscaConversasResponse struct {
	Resultados []resultadoBuscaResponse `json:"resultados"`
}

// Buscar atende GET /v1/conversas/busca?caixa=&q=&limite=: as conversas com
// mensagem que contem `q`, cada uma com a ultima mensagem que casa e o
// trecho em volta. Quem decide se o usuario pode ver a conversa e a
// aplicacao, como no resto do /v1.
func (h *ConversasV1) Buscar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	termo := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(termo) < tamanhoMinimoBusca {
		http.Error(w, "q deve ter ao menos 3 letras", http.StatusBadRequest)
		return
	}

	var caixaID *int64
	if codigo := strings.TrimSpace(q.Get("caixa")); codigo != "" {
		cx, ok := caixaDaRequisicao(w, r, h.caixas, codigo)
		if !ok {
			return
		}
		caixaID = &cx.ID
	}

	limite, ok := inteiroDaQuery(w, r, "limite", limitePadraoBusca)
	if !ok {
		return
	}
	if limite < 1 || limite > limiteMaximoBusca {
		limite = limiteMaximoBusca
	}

	linhas, err := store.New(h.pool).BuscarTextoNasConversas(r.Context(), store.BuscarTextoNasConversasParams{
		CaixaID: caixaID,
		Termo:   escaparLike(termo),
		Limite:  int32(limite),
	})
	if err != nil {
		slog.Error("conversas v1: buscar texto", "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	resp := buscaConversasResponse{Resultados: make([]resultadoBuscaResponse, 0, len(linhas))}
	for _, l := range linhas {
		resp.Resultados = append(resp.Resultados, resultadoBuscaResponse{
			ConversaID: l.ConversaID,
			MensagemID: l.ID,
			Direcao:    l.Direcao,
			Tipo:       l.Tipo,
			Trecho:     trechoDaBusca(deref(l.Texto), termo),
			CriadoEm:   formatarTimestamp(l.CriadoEm),
		})
	}

	responderJSON(w, resp)
}

// trechoDaBusca recorta o texto em volta da primeira ocorrencia do termo.
// A normalizacao troca letra por letra, entao a posicao em runas vale para
// o texto original.
func trechoDaBusca(texto, termo string) string {
	runas := []rune(strings.Join(strings.Fields(texto), " "))
	alvo := []rune(normalizarBusca(strings.Join(strings.Fields(termo), " ")))
	base := []rune(normalizarBusca(string(runas)))

	inicio := 0
	if len(base) == len(runas) {
		if pos := strings.Index(string(base), string(alvo)); pos >= 0 {
			inicio = utf8.RuneCountInString(string(base)[:pos])
		}
	}

	de := max(0, inicio-folgaTrechoBusca)
	ate := min(len(runas), de+tamanhoTrechoMaximo)

	trecho := string(runas[de:ate])
	if de > 0 {
		trecho = "…" + trecho
	}
	if ate < len(runas) {
		trecho += "…"
	}
	return trecho
}
