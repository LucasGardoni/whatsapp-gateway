package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/metrica"
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
	// assinador valida o token de sessao (barramento, fase 3). Nil quando
	// SSE_SIGNING_KEY nao esta configurada -- e ai nenhuma conexao abre,
	// em vez de abrir sem autenticacao (fail closed, padrao da base).
	assinador *sse.AssinadorSessao
	// registro conta conexoes abertas por aplicacao (fase 9). Nil e valido:
	// os testes de SSE nao precisam montar um.
	//
	// Esta rota NAO passa pelo middleware de metricas -- ela e autenticada
	// por token de sessao, nao por token de aplicacao, e mesmo se passasse
	// a contagem de "requisicoes" de um stream de horas nao diria nada. O
	// que interessa aqui e o gauge: quantas telas estao penduradas nesta
	// instancia agora.
	registro *metrica.Registro
}

func NovoEventos(hub *sse.Hub, assinador *sse.AssinadorSessao, origemCRM string, registro *metrica.Registro) *Eventos {
	return &Eventos{hub: hub, assinador: assinador, origemCRM: origemCRM, registro: registro}
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

	if h.assinador == nil {
		http.Error(w, "tempo real nao configurado", http.StatusServiceUnavailable)
		return
	}

	// A chave do hub sai do TOKEN, nunca da query string ou de um header.
	// Se o destino pudesse vir por fora, qualquer um leria o stream de
	// qualquer destino -- a assinatura e o que torna valida aqui dentro a
	// permissao decidida pela aplicacao (secao 1 do plano).
	sessao, err := h.assinador.Validar(r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "token invalido ou expirado", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming nao suportado", http.StatusInternalServerError)
		return
	}

	ch, cancelar := h.hub.Assinar(sessao.Chave())
	defer cancelar()

	// o par +1/-1 fica colado, e o -1 e defer: um gauge que so sobe mente
	// para sempre, e a mentira cresce com o uso normal (toda tela fechada
	// deixaria um resto). Sobe DEPOIS do Assinar para nao contar conexao
	// que nem chegou a existir. So o App entra -- o destino e opaco, e um
	// contador por destino diria quem esta online sem nunca ler mensagem.
	if h.registro != nil {
		h.registro.SSEAberta(sessao.App, 1)
		defer h.registro.SSEAberta(sessao.App, -1)
	}

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
