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
	hub    *sse.Hub
	tokens *sse.TokenStore
}

func NovoEventos(hub *sse.Hub, tokens *sse.TokenStore) *Eventos {
	return &Eventos{hub: hub, tokens: tokens}
}

func (h *Eventos) Servir(w http.ResponseWriter, r *http.Request) {
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
