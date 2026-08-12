// package config le configuracao via variaveis de ambiente sem segredo em codigo
package config

import (
	"fmt"
	"net/url"
	"os"
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

	MidiaDir string

	PublicBaseURL string

	// GatewayServiceToken autentica chamadas de servico do CRM (POST
	// /api/mensagens, POST /api/sessoes-sse -- fase 7). Vazio desliga
	// esses endpoints (fail closed, ver internal/http/middleware).
	GatewayServiceToken string

	// DLPDominiosPermitidos sao os dominios da empresa, alem do host de
	// PublicBaseURL -- link fora dessa lista e bloqueado (secao 6).
	DLPDominiosPermitidos []string
	// DLPSomenteAvisar rebaixa todo bloqueio do dlp para aviso -- liga isso
	// nas 2 semanas de entrada em producao descritas na secao 6.
	DLPSomenteAvisar bool
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

		MidiaDir: getEnv("MIDIA_DIR", "./dados/midia"),

		PublicBaseURL: getEnv("PUBLIC_BASE_URL", "http://localhost:8080"),

		GatewayServiceToken: os.Getenv("GATEWAY_SERVICE_TOKEN"),

		DLPDominiosPermitidos: getLista("DLP_DOMINIOS_PERMITIDOS"),
		DLPSomenteAvisar:      os.Getenv("DLP_SOMENTE_AVISAR") == "true",
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("carregar config: variável de ambiente DATABASE_URL é obrigatória")
	}

	if host := hostDe(cfg.PublicBaseURL); host != "" {
		cfg.DLPDominiosPermitidos = append(cfg.DLPDominiosPermitidos, host)
	}

	return cfg, nil
}

func getEnv(chave, padrao string) string {
	if v := os.Getenv(chave); v != "" {
		return v
	}
	return padrao
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
