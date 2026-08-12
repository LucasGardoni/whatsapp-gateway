package sse

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// validadeToken e curta de proposito -- o token so serve pra abrir UMA
// conexao EventSource, nao pra mante-la (proposta registrada no plano do
// CRM, secao 4.4). O EventSource do browser nao manda header nem cookie
// de sessao do CRM (dominio/porta diferentes), entao esse token e como o
// gateway sabe qual corretor esta do outro lado do stream.
const validadeToken = 5 * time.Minute

type tokenInfo struct {
	corretorID int64
	expiraEm   time.Time
}

// TokenStore guarda os tokens de sessao SSE em memoria -- instancia
// unica do gateway (secao 1 do plano), sem necessidade de persistir.
type TokenStore struct {
	mu     sync.Mutex
	tokens map[string]tokenInfo
}

func NovoTokenStore() *TokenStore {
	return &TokenStore{tokens: make(map[string]tokenInfo)}
}

// Emitir gera um token opaco para o corretor informado. Chamado so pelo
// endpoint de servico (protegido por GATEWAY_SERVICE_TOKEN), nunca
// exposto diretamente ao browser.
func (s *TokenStore) Emitir(corretorID int64) (token string, expiraEm time.Time, err error) {
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return "", time.Time{}, fmt.Errorf("gerar token sse: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	expiraEm = time.Now().Add(validadeToken)

	s.mu.Lock()
	s.limparExpirados()
	s.tokens[token] = tokenInfo{corretorID: corretorID, expiraEm: expiraEm}
	s.mu.Unlock()

	return token, expiraEm, nil
}

// Validar consome o token -- uso unico, so serve para abrir a conexao
// EventSource. Uma reconexao do browser precisa de um token novo (o CRM
// trata isso no onerror do EventSource, nao o gateway). Token desconhecido
// ou expirado retorna ok=false.
func (s *TokenStore) Validar(token string) (corretorID int64, ok bool) {
	if token == "" {
		return 0, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	info, existe := s.tokens[token]
	delete(s.tokens, token) // uso unico -- some mesmo se expirado
	if !existe {
		return 0, false
	}
	if time.Now().After(info.expiraEm) {
		return 0, false
	}
	return info.corretorID, true
}

// limparExpirados varre o mapa a cada emissao -- evita crescimento sem
// limite quando um corretor pede token e nunca chega a conectar o
// EventSource. Chamado com s.mu ja travado.
func (s *TokenStore) limparExpirados() {
	agora := time.Now()
	for token, info := range s.tokens {
		if agora.After(info.expiraEm) {
			delete(s.tokens, token)
		}
	}
}
