package config

import (
	"bufio"
	"os"
	"strings"
)

type Config struct {
	Port           string
	AllowedOrigins []string
	DBDriver       string
	DBHost         string
	DBPort         string
	DBName         string
	DBUser         string
	DBPass         string
	AccountsURL    string
	OracleURL      string
	IntegrationURL string
	ServiceAPIKey  string
	DeliveryURL    string
	DeliveryAPIKey string
	EmailFrom      string
	AppURL         string
}

func Load() *Config {
	loadEnvFile(".env")

	origins := strings.Split(env("ALLOWED_ORIGINS", "http://localhost:1420,tauri://localhost,https://lisaos.dev"), ",")
	for i := range origins {
		origins[i] = strings.TrimSpace(origins[i])
	}

	return &Config{
		Port:           env("PORT", "8010"),
		AllowedOrigins: origins,
		DBDriver:       env("DB_DRIVER", "mysql"),
		DBHost:         env("DB_HOST", "localhost"),
		DBPort:         env("DB_PORT", "3306"),
		DBName:         env("DB_NAME", "construct_source"),
		DBUser:         env("DB_USER", "root"),
		DBPass:         env("DB_PASS", ""),
		AccountsURL:    env("ACCOUNTS_URL", "https://accounts.lisaos.dev"),
		OracleURL:      env("ORACLE_URL", "https://oracle.lisaos.dev"),
		IntegrationURL: env("INTEGRATION_URL", "https://integration-api.lisaos.dev"),
		ServiceAPIKey:  env("SERVICE_API_KEY", ""),
		DeliveryURL:    env("DELIVERY_URL", ""),
		DeliveryAPIKey: env("DELIVERY_API_KEY", ""),
		EmailFrom:      env("EMAIL_FROM", "Construct <noreply@lisaos.dev>"),
		AppURL:         env("APP_URL", "https://lisaos.dev"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			key := strings.TrimSpace(k)
			val := strings.TrimSpace(v)
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
	}
}
