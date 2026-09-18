// package auditoria encadeia por hash cada mensagem gravada (secao 2,
// defesa no 4: "Auditoria encadeada por hash" -- mesmo padrao do
// fmPontoLog do Portal Lider). Cobre os dois pontos do gateway que criam
// uma linha em mensagem: o webhook z-api (entrada) e POST /api/mensagens
// (saida). Se um caminho novo passar a criar mensagem sem chamar
// RegistrarHash, a cadeia fica com um elo faltando -- ver nota em
// registrar.go.
package auditoria

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// ChaveUltimoHash mora em `parametro` (secao 7) -- ponto de partida da
// cadeia (semeado por migration, ver TravarUltimoHashAuditoria).
const ChaveUltimoHash = "auditoria_ultimo_hash"

// Repositorio e o subconjunto de store.Queries que a auditoria precisa.
// Quem chama RegistrarHash deve passar uma instancia ligada a mesma
// transacao do INSERT em mensagem -- a trava e o commit tem que ser
// atomicos com a criacao da mensagem, senao duas mensagens concorrentes
// podem gravar o mesmo hash_anterior.
type Repositorio interface {
	TravarUltimoHashAuditoria(ctx context.Context, chave string) (*string, error)
	AtualizarHashMensagem(ctx context.Context, arg store.AtualizarHashMensagemParams) error
	DefinirParametro(ctx context.Context, arg store.DefinirParametroParams) error
}

// encadear calcula hash = sha256(hashAnterior|campo1|campo2|...) -- o
// separador evita que ("ab","c") e ("a","bc") produzam o mesmo hash.
func encadear(hashAnterior string, campos []string) string {
	h := sha256.New()
	h.Write([]byte(hashAnterior))
	for _, c := range campos {
		h.Write([]byte{'|'})
		h.Write([]byte(c))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RegistrarHash trava o ultimo hash da cadeia, calcula o novo elo a partir
// dos campos estaveis da mensagem (o que nunca muda depois de criada -- ver
// chamadores) e grava tanto em mensagem quanto no cursor em `parametro`.
// campos deve incluir o id da mensagem (unico e imutavel) pra garantir que
// duas mensagens com conteudo identico nao colidam no mesmo hash.
func RegistrarHash(ctx context.Context, repo Repositorio, mensagemID int64, campos ...string) error {
	anteriorPtr, err := repo.TravarUltimoHashAuditoria(ctx, ChaveUltimoHash)
	if err != nil {
		return fmt.Errorf("auditoria: travar ultimo hash: %w", err)
	}
	var anterior string
	if anteriorPtr != nil {
		anterior = *anteriorPtr
	}

	novo := encadear(anterior, campos)

	if err := repo.AtualizarHashMensagem(ctx, store.AtualizarHashMensagemParams{
		ID:           mensagemID,
		HashAnterior: naoVazio(anterior),
		Hash:         &novo,
	}); err != nil {
		return fmt.Errorf("auditoria: atualizar hash da mensagem %d: %w", mensagemID, err)
	}

	if err := repo.DefinirParametro(ctx, store.DefinirParametroParams{
		Chave: ChaveUltimoHash,
		Valor: &novo,
	}); err != nil {
		return fmt.Errorf("auditoria: avancar cursor da cadeia: %w", err)
	}
	return nil
}

// VersaoFormula e a versao da composicao do hash (barramento, fase 2). A
// 1 nao tinha `origem`; a 2 tem. As duas convivem na mesma tabela: quem
// for verificar a cadeia precisa saber qual formula aplicar a cada trecho,
// e e o parametro ChaveFormulaAPartirDeMensagemID que diz onde e o corte.
//
// Mudar a formula de novo exige: subir este numero, gravar um corte novo e
// atualizar docs/AUDITORIA-INTEGRACAO.md. Nao ha migracao de hash antigo
// -- recalcular seria justamente o que a cadeia existe para impedir.
const VersaoFormula = 2

// ChaveVersaoFormula e ChaveFormulaAPartirDeMensagemID moram em
// `parametro` e sao o que torna a troca de formula verificavel depois.
const (
	ChaveVersaoFormula              = "auditoria_versao_formula"
	ChaveFormulaAPartirDeMensagemID = "auditoria_formula_2_a_partir_de_mensagem_id"
)

// Mensagem sao os campos estaveis que entram no hash -- o que nunca muda
// depois de a mensagem ser criada.
//
// E struct, e nao uma lista de parametros posicionais, porque a ordem dos
// campos AQUI e a formula: trocar dois `string` adjacentes numa chamada
// compilaria, passaria nos testes de handler e quebraria a cadeia em
// silencio. Com nome de campo, o erro nao tem como acontecer.
type Mensagem struct {
	ID         int64
	ConversaID int64
	// Direcao e "entrada" ou "saida".
	Direcao       string
	Tipo          string
	Texto         string
	MidiaCaminho  string
	ProvedorMsgID string
	// Origem e a procedencia (fase 2): o codigo da aplicacao autenticada
	// nas mensagens de saida, o provedor nas de entrada. Vazia no caminho
	// legado, que nao identifica aplicacao -- some na fase 8.
	//
	// Nunca vem do corpo da requisicao. Se viesse, o Portal poderia assinar
	// uma mensagem como se fosse o CRM, e a cadeia passaria a provar uma
	// procedencia falsa com toda a aparencia de verdadeira.
	Origem string
}

// CamposMensagem monta os campos estaveis na ordem certa -- exportado pra
// os dois chamadores (webhook zapi e handler de mensagens) nao divergirem
// na composicao do hash. Campo vazio (ex.: MidiaCaminho numa mensagem de
// texto) so entra como string vazia, sem pular posicao -- a ordem e que
// da o significado de cada campo no hash.
func CamposMensagem(m Mensagem) []string {
	return []string{
		fmt.Sprintf("%d", m.ID),
		fmt.Sprintf("%d", m.ConversaID),
		m.Direcao,
		m.Tipo,
		m.Texto,
		m.MidiaCaminho,
		m.ProvedorMsgID,
		m.Origem,
	}
}

func naoVazio(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
