// package mensagem valida o que entra pelo barramento antes de virar
// linha no banco (docs/PLANO_BARRAMENTO_MENSAGENS.md, fase 5).
//
// A regra que rege este pacote inteiro: ele valida TAMANHO e PRESENCA,
// nunca conteudo (secao 4). Nao existe aqui, e nao pode passar a existir,
// nenhuma funcao que olhe dentro do que a aplicacao mandou -- no caso da
// mensagem interna isso nem seria possivel, porque o conteudo chega
// cifrado com uma chave que nunca passa pelo gateway.
//
// Se um dia aparecer neste pacote uma checagem sobre o que a mensagem
// DIZ, houve vazamento de regra de negocio: volte a secao 2 do plano.
package mensagem

import (
	"errors"
	"fmt"
)

// CifraAES256GCM e hoje o unico algoritmo aceito (secao 4: AES-256-GCM,
// nonce de 12 bytes prefixado ao ciphertext).
//
// A lista e fechada de proposito. Aceitar qualquer string aqui faria o
// gateway guardar um rotulo que ninguem conferiu, e a aplicacao so
// descobriria o erro de digitacao na hora de decifrar -- possivelmente
// meses depois, com o historico inteiro ilegivel.
const CifraAES256GCM = "aes-256-gcm"

// molduraCifra e o tamanho minimo do ciphertext de cada algoritmo: o que
// a moldura ocupa quando o texto claro e vazio.
//
// Isso NAO e inspecao de conteudo -- e a mesma validacao de tamanho de
// sempre, so que o minimo depende do algoritmo declarado. Serve para
// pegar dois erros comuns de integracao na entrada, e nao seis meses
// depois: ciphertext truncado por um buffer e, o mais caro, texto claro
// curto mandado no campo errado.
var molduraCifra = map[string]int{
	// 12 bytes de nonce + 16 bytes de tag GCM.
	CifraAES256GCM: 12 + 16,
}

// TamanhoMaximoConteudoCifradoPadrao limita o blob por mensagem quando
// nem o processo nem a aplicacao dizem outra coisa.
//
// Conservador de proposito: mensagem de chat cifrada nao chega perto
// disso, e sem teto uma aplicacao com bug de laco enche a tabela que todo
// mundo compartilha.
//
// Desde a fase 9 o teto e configuravel em dois niveis --
// LIMITE_CONTEUDO_CIFRADO_BYTES no processo e
// aplicacao.limite_conteudo_cifrado_bytes por aplicacao. O default subiu
// de constante para piso: e o que vale quando os dois estao ausentes, e
// e por isso que ele continua conservador. Um default generoso seria o
// limite real de quem nunca configurou nada.
const TamanhoMaximoConteudoCifradoPadrao = 64 << 10 // 64KB

// TamanhoMaximoRemetente casa com o teto de canal_externo e destino
// (handler.tamanhoMaximoIdentificador): sao todos identificadores opacos
// da aplicacao, e divergir daria a mesma string dois limites diferentes
// dependendo da rota.
const TamanhoMaximoRemetente = 256

// ErroValidacao marca o que o chamador deve devolver como 400 -- a
// mensagem ja e escrita para ser lida por quem integra, entao vai inteira
// para a resposta. Erro que nao e deste tipo e falha do gateway, e nao
// da requisicao.
type ErroValidacao struct{ Motivo string }

func (e ErroValidacao) Error() string { return e.Motivo }

func invalido(formato string, args ...any) error {
	return ErroValidacao{Motivo: fmt.Sprintf(formato, args...)}
}

// EhValidacao diz se o erro veio da requisicao (400) ou do gateway (500).
func EhValidacao(err error) bool {
	var alvo ErroValidacao
	return errors.As(err, &alvo)
}

// Interna e uma mensagem do canal interno, como a aplicacao a enviou.
//
// Note o que NAO tem aqui: texto, autor, destinatario, assunto. O
// remetente e uma string opaca, o canal e uma string opaca e o conteudo e
// um blob que o gateway nao abre.
type Interna struct {
	CanalExterno    string
	Remetente       string
	ConteudoCifrado []byte
	CifraAlg        string
	CifraVersao     int32
}

// Validar recusa o que nao da para gravar de forma util. Toda falha aqui
// e ErroValidacao.
//
// limiteConteudoCifrado <= 0 cai no default (fase 9). O limite entra como
// PARAMETRO e nao como campo da Interna porque nao e propriedade da
// mensagem: a mesma mensagem e aceita por uma aplicacao e recusada por
// outra, e guardar o teto junto do conteudo faria parecer que ele viajou
// com ela.
func (m Interna) Validar(limiteConteudoCifrado int) error {
	if limiteConteudoCifrado <= 0 {
		limiteConteudoCifrado = TamanhoMaximoConteudoCifradoPadrao
	}

	switch {
	case m.CanalExterno == "":
		return invalido("canal_externo e obrigatorio")
	case len(m.CanalExterno) > TamanhoMaximoRemetente:
		return invalido("canal_externo excede %d caracteres", TamanhoMaximoRemetente)
	case m.Remetente == "":
		// sem remetente a mensagem chega no canal sem autor nenhum, e a
		// aplicacao nao tem como renderizar nem como auditar depois. O
		// gateway nao sabe QUEM e -- so exige que alguem seja.
		return invalido("remetente e obrigatorio")
	case len(m.Remetente) > TamanhoMaximoRemetente:
		return invalido("remetente excede %d caracteres", TamanhoMaximoRemetente)
	}

	moldura, conhecido := molduraCifra[m.CifraAlg]
	if !conhecido {
		return invalido("cifra_alg invalido: use %s", CifraAES256GCM)
	}
	// versao 0 quase sempre e o campo ausente, nao uma versao de chave
	// chamada zero. Recusar na entrada evita o historico nascer sem saber
	// com que chave foi cifrado -- que e o unico dado que torna a rotacao
	// de chave possivel depois (secao 4).
	if m.CifraVersao <= 0 {
		return invalido("cifra_versao e obrigatorio e deve ser maior que zero")
	}

	switch {
	case len(m.ConteudoCifrado) == 0:
		return invalido("conteudo_cifrado e obrigatorio")
	case len(m.ConteudoCifrado) <= moldura:
		return invalido("conteudo_cifrado menor que a moldura de %s (%d bytes): parece truncado ou nao cifrado", m.CifraAlg, moldura)
	case len(m.ConteudoCifrado) > limiteConteudoCifrado:
		// o limite vai na mensagem: quem integra precisa saber o teto que
		// vale para ELE, e nao existe um so -- descobrir isso por
		// tentativa e o que a fase 9 evita ao mandar o numero de volta.
		return invalido("conteudo_cifrado excede %d bytes", limiteConteudoCifrado)
	}

	return nil
}
