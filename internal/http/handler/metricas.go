package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/metrica"
)

// Metricas serve GET /metrics -- o trafego de cada aplicacao no
// barramento (fase 9).
//
// Formato de exposicao do Prometheus, e nao JSON, por dois motivos: e o
// que qualquer coletor le sem adaptador, e e texto de linha, entao um
// `curl` na madrugada tambem serve. Nao entra dependencia nenhuma para
// isso -- o formato sao quatro linhas de fmt.Fprintf (diretriz 3 do
// plano).
//
// Os numeros sao DESTA INSTANCIA. Atras de um proxy com duas instancias,
// cada uma responde a sua parte, e quem le soma -- que e como coletor de
// metrica trabalha de qualquer forma. Centralizar isso no banco custaria
// um INSERT por requisicao (ver internal/metrica).
//
// O que NAO tem aqui: destino, canal_externo, remetente, id de mensagem.
// Metrica e o caminho mais facil de furar a secao 2 do plano sem
// perceber, porque parece inofensiva -- um contador por destino diria
// quem conversa com quem sem nunca abrir uma mensagem.
type Metricas struct {
	registro *metrica.Registro
}

func NovoMetricas(registro *metrica.Registro) *Metricas {
	return &Metricas{registro: registro}
}

func (h *Metricas) Servir(w http.ResponseWriter, r *http.Request) {
	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		http.Error(w, "nao autorizado", http.StatusUnauthorized)
		return
	}

	// a resposta mostra o trafego de TODAS as aplicacoes, entao ter token
	// nao basta: seria uma aplicacao lendo o movimento das outras, que e
	// a fronteira que este plano inteiro existe para manter. Quem le e
	// dado (aplicacao.pode_ler_metricas), nunca um codigo escrito aqui.
	if !app.PodeLerMetricas {
		http.Error(w, "esta aplicacao nao pode ler metricas", http.StatusForbidden)
		return
	}

	// version=0.0.4 e o que o Prometheus espera; sem ele alguns coletores
	// tratam a resposta como texto opaco e nao raspam nada.
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	amostras := h.registro.Amostrar()

	// HELP/TYPE uma vez por metrica, antes de todas as series dela -- e o
	// que o formato exige. Por isso o laco e por metrica e nao por
	// aplicacao, mesmo custando cinco passadas na mesma fatia.
	escreverContador(w, "gateway_requisicoes_por_minuto",
		"requisicoes autenticadas nos ultimos 60s, por aplicacao", "gauge",
		amostras, func(a metrica.Amostra) string {
			return serie(a.Aplicacao, "", a.RequisicoesPorMinuto)
		})
	escreverContador(w, "gateway_requisicoes_total",
		"requisicoes autenticadas desde a subida desta instancia", "counter",
		amostras, func(a metrica.Amostra) string {
			return serie(a.Aplicacao, "", int64(a.RequisicoesTotal))
		})
	escreverContador(w, "gateway_erros_por_minuto",
		"respostas com status >= 400 nos ultimos 60s, por aplicacao", "gauge",
		amostras, func(a metrica.Amostra) string {
			return serie(a.Aplicacao, "", a.ErrosPorMinuto)
		})
	escreverContador(w, "gateway_erros_total",
		"respostas com status >= 400 desde a subida desta instancia", "counter",
		amostras, func(a metrica.Amostra) string {
			return serie(a.Aplicacao, "", int64(a.ErrosTotal))
		})
	escreverContador(w, "gateway_sse_conexoes_abertas",
		"conexoes EventSource abertas nesta instancia, por aplicacao", "gauge",
		amostras, func(a metrica.Amostra) string {
			return serie(a.Aplicacao, "", a.SSEAbertas)
		})
	escreverContador(w, "gateway_mensagens_por_minuto",
		"mensagens aceitas nos ultimos 60s, por aplicacao e canal", "gauge",
		amostras, func(a metrica.Amostra) string {
			var b strings.Builder
			for _, c := range a.Mensagens {
				b.WriteString(serie(a.Aplicacao, c.Canal, c.PorMinuto))
			}
			return b.String()
		})
	escreverContador(w, "gateway_mensagens_total",
		"mensagens aceitas desde a subida desta instancia, por aplicacao e canal", "counter",
		amostras, func(a metrica.Amostra) string {
			var b strings.Builder
			for _, c := range a.Mensagens {
				b.WriteString(serie(a.Aplicacao, c.Canal, int64(c.Total)))
			}
			return b.String()
		})
}

func escreverContador(w http.ResponseWriter, nome, ajuda, tipo string, amostras []metrica.Amostra, linha func(metrica.Amostra) string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", nome, ajuda, nome, tipo)
	for _, a := range amostras {
		for _, l := range strings.Split(strings.TrimSuffix(linha(a), "\n"), "\n") {
			if l == "" {
				continue
			}
			fmt.Fprintf(w, "%s%s\n", nome, l)
		}
	}
}

// serie monta `{rotulos} valor`. canal vazio omite o rotulo em vez de
// emitir canal="" -- serie com rotulo vazio e serie diferente para o
// Prometheus, e os graficos ficariam com uma linha fantasma.
func serie(aplicacao, canal string, valor int64) string {
	rotulos := `aplicacao="` + escaparRotulo(aplicacao) + `"`
	if canal != "" {
		rotulos += `,canal="` + escaparRotulo(canal) + `"`
	}
	return fmt.Sprintf("{%s} %d\n", rotulos, valor)
}

// escaparRotulo protege o formato de um codigo de aplicacao com aspas,
// barra ou quebra de linha. Os codigos de hoje sao [a-z_], mas quem os
// escolhe e um INSERT feito a mao -- e um valor de rotulo mal escapado
// deixaria quem cria a aplicacao injetar series falsas no painel de
// metricas de todo mundo.
func escaparRotulo(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}
