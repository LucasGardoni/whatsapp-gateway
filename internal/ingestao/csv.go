package ingestao

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/LucasGardoni/whatsapp-gateway/internal/identidade"
	"github.com/LucasGardoni/whatsapp-gateway/internal/store"
)

// RepositorioCSV e o que o importador precisa alem da deduplicacao: gravar
// o bruto de cada linha antes de normalizar (diretriz 7), igual a todo
// outro caminho de entrada deste gateway.
type RepositorioCSV interface {
	Repositorio
	InserirLeadPayloadBruto(ctx context.Context, arg store.InserirLeadPayloadBrutoParams) (int64, error)
	AtualizarLeadDoPayloadBruto(ctx context.Context, arg store.AtualizarLeadDoPayloadBrutoParams) error
}

// ResumoCSV contabiliza o resultado da importacao pra devolver ao
// supervisor que subiu o arquivo (secao "Aceite" da fase 11).
type ResumoCSV struct {
	Total      int      `json:"total"`
	Criados    int      `json:"criados"`
	Duplicados int      `json:"duplicados"`
	Invalidos  int      `json:"invalidos"`
	Erros      []string `json:"erros,omitempty"`
}

// colunasEsperadas -- csv pra "base fria" (leads que ainda nao foram
// contatados), sem a exigencia de telefone ja resolvido em @lid, ja que
// isso so acontece no disparo (secao 4.3).
var colunasEsperadas = []string{"nome", "telefone", "empreendimento_id"}

// ImportarCSV le uma linha por vez, persiste o bruto, deduplica e cria o
// lead -- mesmo pipeline do webhook generico, so que a fonte e um arquivo
// em vez de um payload de webhook (fase 11: "importador csv pra base fria").
// Uma linha invalida nao aborta o arquivo inteiro: conta como invalida e
// segue pra proxima.
func ImportarCSV(ctx context.Context, leitor io.Reader, repo RepositorioCSV, origem string, janela time.Duration) (ResumoCSV, error) {
	leitorCSV := csv.NewReader(leitor)
	leitorCSV.FieldsPerRecord = -1

	cabecalho, err := leitorCSV.Read()
	if err != nil {
		return ResumoCSV{}, fmt.Errorf("importar csv: ler cabecalho: %w", err)
	}
	indices, err := indexarColunas(cabecalho)
	if err != nil {
		return ResumoCSV{}, fmt.Errorf("importar csv: %w", err)
	}

	var resumo ResumoCSV
	for {
		linha, err := leitorCSV.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return resumo, fmt.Errorf("importar csv: ler linha %d: %w", resumo.Total+1, err)
		}
		resumo.Total++

		if err := processarLinhaCSV(ctx, repo, origem, janela, cabecalho, linha, indices, &resumo); err != nil {
			resumo.Invalidos++
			resumo.Erros = append(resumo.Erros, fmt.Sprintf("linha %d: %v", resumo.Total, err))
		}
	}

	return resumo, nil
}

func processarLinhaCSV(ctx context.Context, repo RepositorioCSV, origem string, janela time.Duration, cabecalho, linha []string, indices map[string]int, resumo *ResumoCSV) error {
	bruto, err := linhaParaJSON(cabecalho, linha)
	if err != nil {
		return err
	}

	payloadBrutoID, err := repo.InserirLeadPayloadBruto(ctx, store.InserirLeadPayloadBrutoParams{
		Origem:  origem,
		Payload: bruto,
	})
	if err != nil {
		return fmt.Errorf("persistir bruto: %w", err)
	}

	nome := strings.TrimSpace(valorColuna(linha, indices, "nome"))
	telefoneBruto := strings.TrimSpace(valorColuna(linha, indices, "telefone"))
	if telefoneBruto == "" {
		return fmt.Errorf("telefone ausente")
	}
	telefone, err := identidade.NormalizarE164(telefoneBruto)
	if err != nil {
		return fmt.Errorf("telefone invalido: %w", err)
	}

	var empreendimentoID *int64
	if v := strings.TrimSpace(valorColuna(linha, indices, "empreendimento_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("empreendimento_id invalido: %w", err)
		}
		empreendimentoID = &id
	}

	resultado, err := ResolverOuCriarLead(ctx, repo, Entrada{
		Nome:             nome,
		TelefoneE164:     telefone,
		Origem:           origem,
		EmpreendimentoID: empreendimentoID,
	}, janela)
	if err != nil {
		return err
	}

	if err := repo.AtualizarLeadDoPayloadBruto(ctx, store.AtualizarLeadDoPayloadBrutoParams{
		ID:     payloadBrutoID,
		LeadID: &resultado.LeadID,
	}); err != nil {
		return fmt.Errorf("associar bruto ao lead: %w", err)
	}

	if resultado.Novo {
		resumo.Criados++
	} else {
		resumo.Duplicados++
	}
	return nil
}

// indexarColunas exige nome/telefone -- empreendimento_id e opcional. A
// ordem das colunas no arquivo nao importa, so o nome do cabecalho.
func indexarColunas(cabecalho []string) (map[string]int, error) {
	indices := make(map[string]int, len(cabecalho))
	for i, c := range cabecalho {
		indices[strings.ToLower(strings.TrimSpace(c))] = i
	}
	for _, obrigatoria := range []string{"nome", "telefone"} {
		if _, ok := indices[obrigatoria]; !ok {
			return nil, fmt.Errorf("coluna obrigatoria ausente: %s (esperado: %s)", obrigatoria, strings.Join(colunasEsperadas, ", "))
		}
	}
	return indices, nil
}

func valorColuna(linha []string, indices map[string]int, coluna string) string {
	i, ok := indices[coluna]
	if !ok || i >= len(linha) {
		return ""
	}
	return linha[i]
}

// linhaParaJSON preserva a linha bruta em lead_payload_bruto (diretriz 7)
// -- jsonb pra ficar consistente com o formato dos outros payloads brutos,
// nao csv cru.
func linhaParaJSON(cabecalho, linha []string) ([]byte, error) {
	mapa := make(map[string]string, len(cabecalho))
	for i, c := range cabecalho {
		if i < len(linha) {
			mapa[c] = linha[i]
		}
	}
	corpo, err := json.Marshal(mapa)
	if err != nil {
		return nil, fmt.Errorf("serializar linha: %w", err)
	}
	return corpo, nil
}
