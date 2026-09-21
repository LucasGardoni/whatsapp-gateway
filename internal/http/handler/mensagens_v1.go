package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucasGardoni/whatsapp-gateway/internal/auditoria"
	"github.com/LucasGardoni/whatsapp-gateway/internal/http/middleware"
	"github.com/LucasGardoni/whatsapp-gateway/internal/mensagem"
	"github.com/LucasGardoni/whatsapp-gateway/internal/sse"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// MensagensV1 e a entrada do barramento (fase 5): o gateway deixa de so
// ler mensagem interna e passa a escreve-la.
//
// O que ele grava e um blob que ele NAO consegue abrir -- a chave fica no
// backend da aplicacao e nunca transita por aqui (secao 4 do plano). Isso
// nao e zelo: e o que torna tecnicamente impossivel, e nao apenas
// proibido, implementar regra sobre o conteudo de uma mensagem interna.
// Se um dia aparecer neste arquivo qualquer coisa que leia o conteudo, o
// modelo foi quebrado.
//
// Na fase 6 este handler ganha canal=whatsapp e o Entregador polimorfico;
// hoje ele so atende canal=interno.
type MensagensV1 struct {
	pool *pgxpool.Pool
	// Hub e opcional -- nil grava normalmente e nao notifica ninguem em
	// tempo real, equivalente a nao ter SSE configurado.
	Hub *sse.Hub
}

func NovoMensagensV1(pool *pgxpool.Pool) *MensagensV1 {
	return &MensagensV1{pool: pool}
}

// canalInterno e o unico valor aceito hoje em canal. O campo existe desde
// ja (e nao so na fase 6) para a aplicacao nao ter de mudar o payload
// depois: quem escrever "canal": "interno" agora continua valendo quando
// "whatsapp" entrar.
const canalInterno = "interno"

// limiteHistoricoPadrao e limiteHistoricoMaximo paginam o historico. Sem
// teto, um limite grande transforma uma leitura de tela num dump do canal
// inteiro -- e o conteudo e cifrado, entao o custo cai todo em banda e
// memoria sem nem servir para nada.
const (
	limiteHistoricoPadrao = 50
	limiteHistoricoMaximo = 500
)

type criarMensagemV1Request struct {
	Canal string `json:"canal"`
	// CanalExterno, Remetente e ConteudoCifrado sao opacos: strings cujo
	// significado pertence a aplicacao.
	CanalExterno string `json:"canal_externo"`
	Remetente    string `json:"remetente"`
	// ConteudoCifrado chega em base64 (nonce || ciphertext). Base64 porque
	// JSON nao carrega bytes -- a decodificacao aqui nao e leitura do
	// conteudo, e transporte.
	ConteudoCifrado string `json:"conteudo_cifrado"`
	CifraAlg        string `json:"cifra_alg"`
	CifraVersao     int32  `json:"cifra_versao"`
}

type criarMensagemV1Response struct {
	ID int64 `json:"id"`
	// Status e "registrada", e nao "pendente" como no WhatsApp.
	//
	// O plano (6.1) previa resposta identica nos dois canais, mas
	// "pendente" e o primeiro estado de uma maquina de entrega que o canal
	// interno nao tem: nao ha outbox, nao ha retry e nao ha status que
	// mude depois. Responder "pendente" mandaria a aplicacao esperar uma
	// transicao que nunca chega. A diferenca esta registrada no plano,
	// fase 5.
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

// Criar atende POST /v1/mensagens.
func (h *MensagensV1) Criar(w http.ResponseWriter, r *http.Request) {
	app, autenticada := middleware.AplicacaoDoContexto(r.Context())
	if !autenticada {
		// defesa em profundidade: a rota ja esta atras do middleware
		// estrito de /v1/*. Sem aplicacao nao ha escopo, e sem escopo a
		// mensagem entraria no canal de alguem que nao se sabe quem e.
		http.Error(w, "nao autorizado", http.StatusUnauthorized)
		return
	}

	var req criarMensagemV1Request
	if err := json.NewDecoder(io.LimitReader(r.Body, tamanhoMaximoPayload)).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}

	switch strings.TrimSpace(req.Canal) {
	case canalInterno:
	case "whatsapp":
		// dizer onde esta o caminho que funciona hoje poupa a equipe
		// integradora de descobrir por tentativa que /v1 ainda nao cobre
		// WhatsApp. Sai na fase 6, quando passar a cobrir.
		http.Error(w, "canal whatsapp ainda nao atendido em /v1/mensagens: use POST /api/mensagens", http.StatusBadRequest)
		return
	default:
		http.Error(w, "canal e obrigatorio: use interno", http.StatusBadRequest)
		return
	}

	// base64 invalido e erro de transporte, nao de conteudo -- o gateway
	// nao esta olhando o que ha dentro, so desfazendo o envelope que o
	// JSON exigiu.
	conteudo, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ConteudoCifrado))
	if err != nil {
		http.Error(w, "conteudo_cifrado deve ser base64", http.StatusBadRequest)
		return
	}

	msg := mensagem.Interna{
		CanalExterno:    strings.TrimSpace(req.CanalExterno),
		Remetente:       strings.TrimSpace(req.Remetente),
		ConteudoCifrado: conteudo,
		CifraAlg:        strings.TrimSpace(req.CifraAlg),
		CifraVersao:     req.CifraVersao,
	}
	if err := msg.Validar(); err != nil {
		if mensagem.EhValidacao(err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		slog.Error("mensagens v1: validar", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	// tx envolve o insert e o elo de auditoria -- os dois confirmam
	// juntos, senao um crash entre eles deixa a mensagem fora da cadeia.
	// Mesmo padrao de handler/mensagens.go.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("mensagens v1: iniciar transacao", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	queries := store.New(tx)

	linha, err := queries.CriarMensagemInterna(ctx, store.CriarMensagemInternaParams{
		AplicacaoID:      app.ID,
		CanalExterno:     msg.CanalExterno,
		RemetenteExterno: &msg.Remetente,
		ConteudoCifrado:  msg.ConteudoCifrado,
		CifraAlg:         &msg.CifraAlg,
		CifraVersao:      &msg.CifraVersao,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// o INSERT ... SELECT nao acha canal: ou nao existe, ou e de outra
		// aplicacao. As duas respondem igual, de proposito -- distinguir
		// confirmaria a existencia do canal alheio.
		http.Error(w, "canal nao encontrado", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("mensagens v1: criar mensagem interna", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// canal_ref_id nunca volta nulo aqui: a linha acabou de ser inserida a
	// partir de uma linha de canal. Conferido mesmo assim porque a
	// alternativa seria desreferenciar e derrubar o processo.
	if linha.CanalRefID == nil {
		slog.Error("mensagens v1: mensagem gravada sem canal", "aplicacao", app.Codigo, "mensagem_id", linha.ID)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	canalID := *linha.CanalRefID

	// a cadeia encadeia o CIPHERTEXT (secao 4, "Auditoria sob cifra"):
	// continua provando ordem e integridade, e deixa de provar conteudo
	// sem a chave da aplicacao -- que e o esperado no modelo (a), nao uma
	// perda acidental.
	if err := auditoria.RegistrarHashInterna(ctx, queries, linha.ID,
		auditoria.CamposMensagemInterna(auditoria.MensagemInterna{
			ID:              linha.ID,
			CanalID:         canalID,
			Remetente:       msg.Remetente,
			CifraAlg:        msg.CifraAlg,
			CifraVersao:     msg.CifraVersao,
			ConteudoCifrado: msg.ConteudoCifrado,
			Origem:          app.Codigo,
		})...,
	); err != nil {
		slog.Error("mensagens v1: registrar hash", "mensagem_id", linha.ID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// a lista de entrega e lida DENTRO da transacao: quem foi removido do
	// canal no mesmo instante nao deve receber, e ler depois do commit
	// abriria essa janela.
	chaves, err := queries.ListarChavesDeEntregaDoCanal(ctx, canalID)
	if err != nil {
		slog.Error("mensagens v1: listar destinos do canal", "canal_id", canalID, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("mensagens v1: commit", "aplicacao", app.Codigo, "erro", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	// publica so depois do commit: ninguem pode ser avisado de uma
	// mensagem que a transacao acabou descartando. Na fase 7 esta
	// publicacao direta sai e a fonte unica passa a ser o pg_notify --
	// manter as duas duplicaria o evento para quem enviou.
	if h.Hub != nil {
		h.Hub.Publicar(chaves, sse.Evento{
			Tipo:         sse.EventoMensagemInternaNova,
			MensagemID:   linha.ID,
			CanalExterno: msg.CanalExterno,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(criarMensagemV1Response{ID: linha.ID, Status: "registrada"})
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
			Remetente:       textoOuVazio(l.RemetenteExterno),
			ConteudoCifrado: base64.StdEncoding.EncodeToString(l.ConteudoCifrado),
			CifraAlg:        textoOuVazio(l.CifraAlg),
			CifraVersao:     inteiroOuZero(l.CifraVersao),
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

func textoOuVazio(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func inteiroOuZero(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}
