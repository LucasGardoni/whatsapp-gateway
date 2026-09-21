package auditoria

import (
	"context"
	"encoding/hex"
	"strconv"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// ChaveUltimoHashInterna e a ponta da cadeia de `mensagem_interna`
// (barramento, fase 5). Cadeia SEPARADA da de `mensagem`, semeada por
// migrations/00019_mensagem_interna_opaca.sql.
//
// Por que separada: verificar uma cadeia exige percorrer os elos na ordem
// em que foram criados, e essa ordem, dentro de uma tabela, e o id. Entre
// duas tabelas com sequences independentes essa ordem nao existe -- so
// `criado_em`, que empata no mesmo instante e nao desempata. Uma cadeia
// unica sobre as duas tabelas ficaria com elos que ninguem consegue
// reordenar, ou seja, deixaria de provar o que a cadeia existe para
// provar.
//
// Duas cadeias, cada uma verificavel por id na sua tabela, provam o mesmo
// que a unica provaria: que nada foi inserido, removido ou alterado
// naquela tabela.
const ChaveUltimoHashInterna = "auditoria_ultimo_hash_interna"

// RepositorioInterna e o subconjunto de store.Queries da cadeia interna.
// Vale a mesma regra do Repositorio: a instancia tem de estar ligada a
// MESMA transacao do INSERT, senao duas mensagens concorrentes gravam o
// mesmo hash_anterior.
type RepositorioInterna interface {
	TravarUltimoHashAuditoria(ctx context.Context, chave string) (*string, error)
	AtualizarHashMensagemInterna(ctx context.Context, arg store.AtualizarHashMensagemInternaParams) error
	DefinirParametro(ctx context.Context, arg store.DefinirParametroParams) error
}

// RegistrarHashInterna encadeia uma mensagem interna.
func RegistrarHashInterna(ctx context.Context, repo RepositorioInterna, mensagemID int64, campos ...string) error {
	return registrarElo(ctx, repo, ChaveUltimoHashInterna, campos, func(anterior *string, novo string) error {
		return repo.AtualizarHashMensagemInterna(ctx, store.AtualizarHashMensagemInternaParams{
			ID:           mensagemID,
			HashAnterior: anterior,
			Hash:         &novo,
		})
	})
}

// MensagemInterna sao os campos estaveis da mensagem interna. Mesma
// disciplina de Mensagem: struct, e nao parametros posicionais, porque a
// ordem dos campos E a formula.
//
// Note que o conteudo entra CIFRADO. A cadeia continua provando ordem e
// integridade -- que nada foi inserido, removido ou alterado. Ela deixa
// de provar conteudo por si so: para isso passa a ser necessaria a chave
// da aplicacao, que nunca passa pelo gateway. Isso e o esperado no modelo
// (a) da secao 4 do plano, nao uma perda acidental.
type MensagemInterna struct {
	ID      int64
	CanalID int64
	// Remetente e o destino opaco de quem enviou. O gateway nao sabe quem e.
	Remetente   string
	CifraAlg    string
	CifraVersao int32
	// ConteudoCifrado entra no hash em hex -- e o blob como foi gravado,
	// byte a byte. Hex, e nao os bytes crus, para o elo ser reproduzivel a
	// mao a partir de um dump da coluna, que e como uma pericia vai
	// conferir isso.
	ConteudoCifrado []byte
	// Origem e o codigo da aplicacao autenticada, derivado do token e
	// nunca do corpo -- mesma regra da fase 2.
	Origem string
}

// VersaoFormulaInterna comeca em 1: a cadeia de mensagem_interna nasce
// agora e nao tem historico sob formula anterior. Nao ha parametro de
// corte para ela porque nao ha corte -- se um dia a formula mudar, sobe
// este numero e grava-se o corte, igual a fase 2 fez para `mensagem`.
const VersaoFormulaInterna = 1

// CamposMensagemInterna monta os campos na ordem que define a formula 1
// da cadeia interna:
//
//	id | canal_ref_id | remetente_externo | cifra_alg | cifra_versao | hex(conteudo_cifrado) | origem
func CamposMensagemInterna(m MensagemInterna) []string {
	return []string{
		strconv.FormatInt(m.ID, 10),
		strconv.FormatInt(m.CanalID, 10),
		m.Remetente,
		m.CifraAlg,
		strconv.FormatInt(int64(m.CifraVersao), 10),
		hex.EncodeToString(m.ConteudoCifrado),
		m.Origem,
	}
}
