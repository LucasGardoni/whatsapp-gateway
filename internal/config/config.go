// package config le configuracao via variaveis de ambiente sem segredo em codigo
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port string

	DatabaseURL string

	ZAPIInstanceID    string
	ZAPIInstanceToken string
	ZAPIClientToken   string

	MetaPhoneNumberID string
	MetaAccessToken   string

	// MetaWebhookVerifyToken autentica o handshake GET que a Meta exige
	// antes de aceitar mandar POST pro webhook de leads (fase 11). Vazio
	// desliga a verificacao (fail closed, mesmo padrao do GatewayServiceToken).
	MetaWebhookVerifyToken string

	MidiaDir string

	PublicBaseURL string

	// GatewayServiceToken autentica chamadas de servico do CRM (POST
	// /api/mensagens, POST /api/sessoes-sse, POST /disparos -- fase 7).
	// Vazio desliga esses endpoints (fail closed, ver
	// internal/http/middleware).
	GatewayServiceToken string

	// WebhookPathSecret e o segredo compartilhado no path dos webhooks de
	// entrada (P1-10). Quem chama esses endpoints -- painel da Z-API, Meta,
	// Zapier -- nao manda header de autenticacao, so uma URL, entao o
	// segredo vive no path. Vazio devolve 404 nos webhooks (fail closed):
	// sem ele, expor o gateway na internet e gravacao de dados aberta.
	WebhookPathSecret string

	// DLPDominiosPermitidos sao os dominios da empresa, alem do host de
	// PublicBaseURL -- link fora dessa lista e bloqueado (secao 6).
	DLPDominiosPermitidos []string
	// DLPSomenteAvisar rebaixa todo bloqueio do dlp para aviso -- liga isso
	// nas 2 semanas de entrada em producao descritas na secao 6.
	DLPSomenteAvisar bool

	// RateLimitPorMinuto protege os endpoints publicos (sem token de
	// servico) contra abuso (fase 12). <= 0 desliga o limite.
	RateLimitPorMinuto int
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:              getEnv("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		ZAPIInstanceID:    os.Getenv("ZAPI_INSTANCE_ID"),
		ZAPIInstanceToken: os.Getenv("ZAPI_INSTANCE_TOKEN"),
		ZAPIClientToken:   os.Getenv("ZAPI_CLIENT_TOKEN"),

		MetaPhoneNumberID: os.Getenv("META_PHONE_NUMBER_ID"),
		MetaAccessToken:   os.Getenv("META_ACCESS_TOKEN"),

		MetaWebhookVerifyToken: os.Getenv("META_WEBHOOK_VERIFY_TOKEN"),

		MidiaDir: getEnv("MIDIA_DIR", "./dados/midia"),

		PublicBaseURL: getEnv("PUBLIC_BASE_URL", "http://localhost:8080"),

		GatewayServiceToken: os.Getenv("GATEWAY_SERVICE_TOKEN"),

		WebhookPathSecret: os.Getenv("WEBHOOK_PATH_SECRET"),

		DLPDominiosPermitidos: getLista("DLP_DOMINIOS_PERMITIDOS"),
		DLPSomenteAvisar:      os.Getenv("DLP_SOMENTE_AVISAR") == "true",

		RateLimitPorMinuto: getInt("RATE_LIMIT_POR_MINUTO", 60),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("carregar config: variável de ambiente DATABASE_URL é obrigatória")
	}

	if host := hostDe(cfg.PublicBaseURL); host != "" {
		cfg.DLPDominiosPermitidos = append(cfg.DLPDominiosPermitidos, host)
	}

	// o segredo vai virar um segmento de path -- se precisar de escape, a
	// URL que o operador colar no painel da Z-API nao vai casar com a rota,
	// e o sintoma aparece como "webhook nao chega" em vez de erro de
	// configuracao. Melhor falhar aqui.
	if s := cfg.WebhookPathSecret; s != "" && s != url.PathEscape(s) {
		return nil, fmt.Errorf("carregar config: WEBHOOK_PATH_SECRET tem caractere que precisa de escape em URL; use apenas [A-Za-z0-9._~-]")
	}

	return cfg, nil
}

func getEnv(chave, padrao string) string {
	if v := os.Getenv(chave); v != "" {
		return v
	}
	return padrao
}

func getInt(chave string, padrao int) int {
	v, err := strconv.Atoi(os.Getenv(chave))
	if err != nil {
		return padrao
	}
	return v
}

// getLista le uma variavel de ambiente separada por virgula. Usada para
// listas configuraveis como os dominios da empresa (secao 6, dlp).
func getLista(chave string) []string {
	v := os.Getenv(chave)
	if v == "" {
		return nil
	}
	partes := strings.Split(v, ",")
	lista := make([]string, 0, len(partes))
	for _, p := range partes {
		if p = strings.TrimSpace(p); p != "" {
			lista = append(lista, p)
		}
	}
	return lista
}

func hostDe(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
