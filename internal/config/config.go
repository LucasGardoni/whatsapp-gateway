// package config le configuracao via variaveis de ambiente sem segredo em codigo
package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port string

	DatabaseURL string

	ZAPIInstanceID    string
	ZAPIInstanceToken string
	ZAPIClientToken   string

	MetaPhoneNumberID string
	MetaAccessToken   string
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
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("carregar config: variável de ambiente DATABASE_URL é obrigatória")
	}

	return cfg, nil
}

func getEnv(chave, padrao string) string {
	if v := os.Getenv(chave); v != "" {
		return v
	}
	return padrao
}
