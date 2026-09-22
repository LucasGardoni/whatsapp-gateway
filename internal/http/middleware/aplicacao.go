package middleware

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// Aplicacao e o consumidor autenticado do barramento (fase 1, secao 5.1
// do plano). E o unico lugar de onde a procedencia de uma mensagem pode
// sair: se `origem` pudesse vir no corpo da requisicao, o Portal poderia
// gravar mensagem como se fosse o CRM, e procedencia que o chamador
// declara nao prova nada -- numa base com cadeia de hash isso e pior que
// campo ausente, porque parece confiavel.
type Aplicacao struct {
	ID     int64
	Codigo string
	Nome   string

	// Os tres campos abaixo sao POLITICA POR APLICACAO, e vem da tabela
	// (fase 9). E o que a secao 2, item 6 do plano permite: comportamento
	// por aplicacao existe como dado, nunca como ramo em codigo. Afrouxar
	// o limite do Portal e um UPDATE; nao ha, e nao pode passar a haver,
	// um `if` com o codigo da aplicacao em nenhum lugar sob internal/.

	// LimiteRequisicoesPorMinuto nulo = usa o default do processo
	// (RATE_LIMIT_APLICACAO_POR_MINUTO). Ver LimitePorAplicacao.
	LimiteRequisicoesPorMinuto *int32
	// LimiteConteudoCifradoBytes nulo = usa o default do processo
	// (LIMITE_CONTEUDO_CIFRADO_BYTES).
	LimiteConteudoCifradoBytes *int32
	// PodeLerMetricas libera GET /metrics, que mostra o trafego de TODAS
	// as aplicacoes -- por isso e permissao, e nao rota aberta a quem
	// tem token.
	PodeLerMetricas bool
}

type chaveContextoAplicacao struct{}

// ComAplicacao existe para os testes montarem um contexto autenticado sem
// subir banco.
func ComAplicacao(ctx context.Context, app Aplicacao) context.Context {
	return context.WithValue(ctx, chaveContextoAplicacao{}, app)
}

// AplicacaoDoContexto devolve a aplicacao autenticada. O segundo retorno e
// falso nas rotas legadas (ExigirTokenServico), que nao identificam
// aplicacao -- quem chama grava procedencia nula ate a fase 8.
func AplicacaoDoContexto(ctx context.Context) (Aplicacao, bool) {
	app, ok := ctx.Value(chaveContextoAplicacao{}).(Aplicacao)
	return app, ok
}

// BuscadorAplicacao e o subconjunto de store.Queries que o middleware usa.
type BuscadorAplicacao interface {
	BuscarAplicacaoPorTokenHash(ctx context.Context, tokenHash string) (store.BuscarAplicacaoPorTokenHashRow, error)
}

// HashToken e o sha256 hex do token de servico -- e o que fica gravado em
// aplicacao.token_hash. sha256 e nao bcrypt de proposito: e segredo de
// alta entropia gerado por maquina, nao senha de humano, entao nao ha
// dicionario a resistir e bcrypt so somaria latencia a cada requisicao.
func HashToken(token string) string {
	soma := sha256.Sum256([]byte(token))
	return hex.EncodeToString(soma[:])
}

// ttlCacheAplicacao e curto de proposito: e o atraso maximo entre marcar
// uma aplicacao como ativo=false e ela parar de conseguir escrever. Sem
// cache nenhum, toda requisicao do barramento viraria um SELECT.
const ttlCacheAplicacao = 30 * time.Second

// maxEntradasCacheAplicacao limita o dano de um chamador que manda token
// aleatorio em loop -- ver guardar.
const maxEntradasCacheAplicacao = 1024

type entradaCache struct {
	app      Aplicacao
	achou    bool
	expiraEm time.Time
}

// AutenticadorAplicacao valida o Bearer contra a tabela aplicacao.
//
// O cache guarda tambem a resposta negativa. Se so o positivo fosse
// guardado, um chamador com token errado em loop -- que e o caso comum,
// integracao mal configurada -- bateria no banco a cada requisicao,
// exatamente o oposto do que o cache existe para evitar.
type AutenticadorAplicacao struct {
	buscador BuscadorAplicacao
	ttl      time.Duration
	agora    func() time.Time

	mu    sync.Mutex
	cache map[string]entradaCache
}

func NovoAutenticadorAplicacao(buscador BuscadorAplicacao) *AutenticadorAplicacao {
	return &AutenticadorAplicacao{
		buscador: buscador,
		ttl:      ttlCacheAplicacao,
		agora:    time.Now,
		cache:    make(map[string]entradaCache),
	}
}

// Middleware exige `Authorization: Bearer <token da aplicacao>` e injeta a
// aplicacao no contexto.
func (a *AutenticadorAplicacao) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			http.Error(w, "nao autorizado", http.StatusUnauthorized)
			return
		}

		app, err := a.Resolver(r.Context(), token)
		if err != nil {
			slog.Error("aplicacao: resolver token", "erro", err)
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
		if app == nil {
			http.Error(w, "nao autorizado", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(ComAplicacao(r.Context(), *app)))
	})
}

// Resolver devolve nil (sem erro) quando o token nao casa com nenhuma
// aplicacao ativa. Erro aqui e so falha de infraestrutura -- que vira 500,
// nunca 401: responder "nao autorizado" com o banco fora faria o operador
// caçar um problema de credencial que nao existe.
func (a *AutenticadorAplicacao) Resolver(ctx context.Context, token string) (*Aplicacao, error) {
	hash := HashToken(token)

	if entrada, ok := a.doCache(hash); ok {
		if !entrada.achou {
			return nil, nil
		}
		app := entrada.app
		return &app, nil
	}

	linha, err := a.buscador.BuscarAplicacaoPorTokenHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		a.guardar(hash, entradaCache{achou: false})
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// o WHERE ja comparou, mas a igualdade do Postgres depende de collation
	// e para no primeiro byte diferente. Refazer aqui em tempo constante
	// custa nada e mantem a unica comparacao de segredo sob nosso controle.
	if subtle.ConstantTimeCompare([]byte(linha.TokenHash), []byte(hash)) != 1 {
		a.guardar(hash, entradaCache{achou: false})
		return nil, nil
	}

	app := Aplicacao{
		ID:                         linha.ID,
		Codigo:                     linha.Codigo,
		Nome:                       linha.Nome,
		LimiteRequisicoesPorMinuto: linha.LimiteRequisicoesPorMinuto,
		LimiteConteudoCifradoBytes: linha.LimiteConteudoCifradoBytes,
		PodeLerMetricas:            linha.PodeLerMetricas,
	}
	a.guardar(hash, entradaCache{app: app, achou: true})
	return &app, nil
}

// Invalidar derruba o cache de um token -- usado depois de sincronizar o
// token da aplicacao na subida, para a instancia nao servir por ate 30s um
// veredito calculado antes da troca.
func (a *AutenticadorAplicacao) Invalidar(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cache, HashToken(token))
}

func (a *AutenticadorAplicacao) doCache(hash string) (entradaCache, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entrada, ok := a.cache[hash]
	if !ok || a.agora().After(entrada.expiraEm) {
		return entradaCache{}, false
	}
	return entrada, true
}

func (a *AutenticadorAplicacao) guardar(hash string, entrada entradaCache) {
	entrada.expiraEm = a.agora().Add(a.ttl)
	a.mu.Lock()
	defer a.mu.Unlock()

	// guardar o negativo protege o banco, mas transforma o cache em algo
	// que um estranho consegue encher: basta mandar tokens aleatorios, um
	// hash distinto por requisicao. O teto e baixo porque o cache legitimo
	// tem uma entrada por aplicacao -- dezenas, nao milhares.
	if len(a.cache) >= maxEntradasCacheAplicacao {
		agora := a.agora()
		for h, e := range a.cache {
			if agora.After(e.expiraEm) {
				delete(a.cache, h)
			}
		}
		// so vivos e ainda cheio: descarta tudo. Custa um SELECT por
		// aplicacao real na proxima requisicao, e o TTL e de 30s de
		// qualquer forma.
		if len(a.cache) >= maxEntradasCacheAplicacao {
			a.cache = make(map[string]entradaCache, maxEntradasCacheAplicacao)
		}
	}

	a.cache[hash] = entrada
}
