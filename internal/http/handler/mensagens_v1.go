package handler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/mensagem"
	"github.com/LucasGardoni/whatsapp-gateway/internal/metrica"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// MensagensV1 e a entrada unificada do barramento: um endpoint, dois
// canais (fase 6).
//
// O que ele grava no canal interno e um blob que ele NAO consegue abrir --
// a chave fica no backend da aplicacao e nunca transita por aqui (secao 4
// do plano). Isso nao e zelo: e o que torna tecnicamente impossivel, e nao
// apenas proibido, implementar regra sobre o conteudo de uma mensagem
// interna. Se um dia aparecer neste arquivo qualquer coisa que leia o
// conteudo, o modelo foi quebrado.
//
// A diferenca entre os dois canais esta inteira nos Entregadores
// (entregador.go). Aqui so existe o que e comum: autenticar, decodificar,
// escolher pelo DADO `canal` e responder.
type MensagensV1 struct {
	pool *pgxpool.Pool
	// entregadores e um mapa, e nao um switch, porque o canal e dado e nao
	// codigo: adicionar um canal novo e registrar uma entrada, e ninguem
	// precisa achar o `if` certo. A chave nao aceitar valor desconhecido e
	// o que devolve 400 na entrada.
	entregadores map[string]Entregador
	// registro conta mensagem aceita por aplicacao e canal (fase 9). Nil e
	// valido -- os testes que nao tratam de metrica nao precisam montar um.
	registro *metrica.Registro
}

// NovoMensagensV1 recebe limiteConteudoCifrado (LIMITE_CONTEUDO_CIFRADO_BYTES,
// fase 9); zero cai no default conservador do pacote mensagem.
func NovoMensagensV1(pool *pgxpool.Pool, midiaDir string, limiteConteudoCifrado int, registro *metrica.Registro) *MensagensV1 {
	return &MensagensV1{
		pool: pool,
		entregadores: map[string]Entregador{
			mensagem.CanalWhatsApp: entregadorWhatsApp{midiaDir: midiaDir},
			mensagem.CanalInterno:  entregadorInterno{limiteConteudoPadrao: limiteConteudoCifrado},
		},
		registro: registro,
	}
}

// limiteHistoricoPadrao e limiteHistoricoMaximo paginam o historico. Sem
// teto, um limite grande transforma uma leitura de tela num dump do canal
// inteiro -- e o conteudo e cifrado, entao o custo cai todo em banda e
// memoria sem nem servir para nada.
const (
	limiteHistoricoPadrao = 50
	limiteHistoricoMaximo = 500
)

type criarMensagemV1Response struct {
	ID int64 `json:"id"`
	// Status diverge por canal: "pendente" no WhatsApp, primeiro estado da
	// maquina de entrega, e "registrada" no interno, que nao tem maquina
	// de entrega nenhuma. Ver entregador.go e o registro da fase 5.
	Status string `json:"status"`
}

type mensagemHistoricoResponse struct {
	ID              int64  `json:"id"`
	Remetente       string `json:"remetente"`
	ConteudoCifrado string `json:"conteudo_cifrado"`
	CifraAlg        string `json:"cifra_alg"`
	CifraVersao     int32  `json:"cifra_versao"`
	CriadoEm        string `json:"criado_em"`
}

type historicoResponse struct {
	Canal     string                      `json:"canal_externo"`
	Mensagens []mensagemHistoricoResponse `json:"mensagens"`
	// UltimoID e o desde_id da proxima pagina. Vem pronto para a aplicacao
	// nao ter de descobrir que e o id da ultima linha -- e o mesmo valor
	// que ela guarda para retomar depois de uma queda do SSE.
	UltimoID int64 `json:"ultimo_id"`
}

// Criar atende POST /v1/mensagens, nos dois canais.
func (h *MensagensV1) Criar(w http.ResponseWriter, r *http.Request) {
	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		// defesa em profundidade: a rota ja esta atras do middleware
		// estrito de /v1/*. Sem aplicacao nao ha escopo, e sem escopo a
		// mensagem entraria no canal de alguem que nao se sabe quem e.
		http.Error(w, "nao autorizado", http.StatusUnauthorized)
		return
	}

	var req mensagem.Requisicao
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}

	req.Canal = strings.TrimSpace(req.Canal)
	entregador, conhecido := h.entregadores[req.Canal]
	if !conhecido {
		http.Error(w, "canal e obrigatorio: use "+h.canaisAceitos(), http.StatusBadRequest)
		return
	}

	entrega, err := entregar(r.Context(), h.pool, entregador, &app, req)
	if err != nil {
		responderErro(w, err, "rota", "/v1/mensagens", "aplicacao", app.Codigo, "canal", req.Canal)
		return
	}

	// contado DEPOIS de dar certo: a metrica de mensagem mede o que entrou
	// no barramento, nao o que foi tentado. Payload recusado ja aparece em
	// gateway_erros_* pelo middleware, e somar as duas coisas na mesma
	// serie tornaria impossivel distinguir "aplicacao com bug" de
	// "aplicacao movimentada".
	if h.registro != nil {
		h.registro.Mensagem(app.Codigo, req.Canal)
	}

	w.Header().Set("Content-Type", "application/json")
	// 201 nos dois canais (secao 6.1). Ate a fase 8 havia a rota legada
	// /api/mensagens respondendo 200 com a mesma implementacao; ela saiu.
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(criarMensagemV1Response{ID: entrega.ID, Status: entrega.Status})
}

// canaisAceitos monta a lista para a mensagem de erro a partir do mapa, e
// nao de uma string fixa: literal ao lado de mapa envelhece calado, e
// quem integra descobre o canal que existe lendo o codigo do gateway.
func (h *MensagensV1) canaisAceitos() string {
	nomes := make([]string, 0, len(h.entregadores))
	for nome := range h.entregadores {
		nomes = append(nomes, nome)
	}
	sort.Strings(nomes)
	return strings.Join(nomes, " ou ")
}

// Historico atende GET /v1/canais/{canal_externo}/mensagens (secao 6.3).
//
// Devolve o ciphertext como foi gravado. Quem decide se AQUELE usuario
// pode ler o canal e a aplicacao, antes de chamar -- o gateway escopa por
// aplicacao e mais nada. Consultar canal_assinante para autorizar leitura
// seria inventar um modelo de permissao aqui dentro: aquela tabela e
// lista de ENTREGA, nao de acesso.
func (h *MensagensV1) Historico(w http.ResponseWriter, r *http.Request) {
	app, canalExterno, ok := escopoCanal(w, r)
	if !ok {
		return
	}

	desdeID, ok := inteiroDaQuery(w, r, "desde_id", 0)
	if !ok {
		return
	}
	limite, ok := inteiroDaQuery(w, r, "limite", limiteHistoricoPadrao)
	if !ok {
		return
	}
	if limite <= 0 || limite > limiteHistoricoMaximo {
		limite = limiteHistoricoMaximo
	}

	linhas, err := store.New(h.pool).ListarMensagensDoCanal(r.Context(), store.ListarMensagensDoCanalParams{
		AplicacaoID:  app.ID,
		CanalExterno: canalExterno,
		DesdeID:      desdeID,
		Limite:       int32(limite),
	})
	if err != nil {
		slog.Error("mensagens v1: listar historico", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// [] e nunca null: canal sem mensagem e estado normal, e quem consome
	// em JS faria .map em null e quebraria a tela por causa disso.
	resp := historicoResponse{
		Canal:     canalExterno,
		Mensagens: make([]mensagemHistoricoResponse, 0, len(linhas)),
		UltimoID:  desdeID,
	}
	for _, l := range linhas {
		resp.Mensagens = append(resp.Mensagens, mensagemHistoricoResponse{
			ID:              l.ID,
			Remetente:       l.RemetenteExterno,
			ConteudoCifrado: base64.StdEncoding.EncodeToString(l.ConteudoCifrado),
			CifraAlg:        l.CifraAlg,
			CifraVersao:     l.CifraVersao,
			CriadoEm:        l.CriadoEm.Time.Format("2006-01-02T15:04:05"),
		})
		resp.UltimoID = l.ID
	}

	responderJSON(w, resp)
}

// inteiroDaQuery le um parametro numerico da query string. Ausente vale o
// padrao; presente e ilegivel e erro -- silenciar transformaria
// desde_id=abc numa releitura do canal inteiro, que e justamente o que a
// aplicacao estava tentando evitar.
func inteiroDaQuery(w http.ResponseWriter, r *http.Request, nome string, padrao int64) (int64, bool) {
	bruto := strings.TrimSpace(r.URL.Query().Get(nome))
	if bruto == "" {
		return padrao, true
	}
	valor, err := strconv.ParseInt(bruto, 10, 64)
	if err != nil {
		http.Error(w, nome+" deve ser um numero inteiro", http.StatusBadRequest)
		return 0, false
	}
	return valor, true
}
