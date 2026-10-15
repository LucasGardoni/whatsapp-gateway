package auditoria

import (
	"context"
	"fmt"

	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// prefixoAlteracao abre os campos do elo de alteracao. A alteracao entra na
// MESMA cadeia das mensagens (auditoria_ultimo_hash): sem o prefixo, quem
// verifica nao saberia se o elo seguinte e de mensagem ou de mensagem_alteracao.
const prefixoAlteracao = "alteracao"

// RepositorioAlteracao e o subconjunto de store.Queries que o elo de
// alteracao precisa -- sempre na transacao do INSERT em mensagem_alteracao.
type RepositorioAlteracao interface {
	TravarUltimoHashAuditoria(ctx context.Context, chave string) (*string, error)
	AtualizarHashAlteracao(ctx context.Context, arg store.AtualizarHashAlteracaoParams) error
	DefinirParametro(ctx context.Context, arg store.DefinirParametroParams) error
}

// Alteracao sao os campos que entram no hash de uma edicao ou exclusao. Como
// em Mensagem, a ordem dos campos e a formula.
type Alteracao struct {
	ID         int64
	MensagemID int64
	// Acao e "edicao" ou "exclusao".
	Acao string
	// Texto e o texto novo da edicao; vazio na exclusao.
	Texto string
	// Autor e "usuario:808" ou "cliente".
	Autor         string
	ProvedorMsgID string
}

func CamposAlteracao(a Alteracao) []string {
	return []string{
		prefixoAlteracao,
		fmt.Sprintf("%d", a.ID),
		fmt.Sprintf("%d", a.MensagemID),
		a.Acao,
		a.Texto,
		a.Autor,
		a.ProvedorMsgID,
	}
}

// RegistrarHashAlteracao encadeia a alteracao na cadeia das mensagens.
func RegistrarHashAlteracao(ctx context.Context, repo RepositorioAlteracao, alteracaoID int64, campos ...string) error {
	return registrarElo(ctx, repo, ChaveUltimoHash, campos, func(anterior *string, novo string) error {
		return repo.AtualizarHashAlteracao(ctx, store.AtualizarHashAlteracaoParams{
			ID:           alteracaoID,
			HashAnterior: anterior,
			Hash:         &novo,
		})
	})
}
