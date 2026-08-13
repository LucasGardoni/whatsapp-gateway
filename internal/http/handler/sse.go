package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
)

// intervaloHeartbeat mantem a conexao viva atraves de proxies reversos
// (IIS ARR/Caddy, secao 5 do plano) que fecham conexao ociosa.
const intervaloHeartbeat = 25 * time.Second

// Eventos serve o EventSource do CRM (fase 7). Autenticado por token curto
// na query string -- o EventSource do browser nao manda header nem
// cookie de sessao do CRM (dominio/porta diferentes).
type Eventos struct {
	hub *sse.Hub
	// origemCRM e a origem exata autorizada a abrir o stream (P0-03).
	// Vazio nao emite o header -- consumo por curl/servidor nao precisa.
	origemCRM string
	tokens    *sse.TokenStore
}

func NovoEventos(hub *sse.Hub, tokens *sse.TokenStore, origemCRM string) *Eventos {
	return &Eventos{hub: hub, tokens: tokens, origemCRM: origemCRM}
}

func (h *Eventos) Servir(w http.ResponseWriter, r *http.Request) {
	// o header vem antes da validacao do token de proposito: sem ele numa
	// resposta 401, o browser esconde o status atras de um erro de CORS
	// generico e o corretor ve "falha ao conectar" sem causa nenhuma.
	// Vary: Origin porque a resposta muda conforme a origem -- sem isso um
	// proxy pode servir a resposta de uma origem para outra.
	if h.origemCRM != "" {
		w.Header().Set("Access-Control-Allow-Origin", h.origemCRM)
		w.Header().Set("Vary", "Origin")
	}

	corretorID, ok := h.tokens.Validar(r.URL.Query().Get("token"))
	if !ok {
		http.Error(w, "token invalido ou expirado", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming nao suportado", http.StatusInternalServerError)
		return
	}

	ch, cancelar := h.hub.Assinar(corretorID)
	defer cancelar()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(intervaloHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case evento, aberto := <-ch:
			if !aberto {
				return
			}
			corpo, err := json.Marshal(evento)
			if err != nil {
				continue
			}
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(corpo)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}
