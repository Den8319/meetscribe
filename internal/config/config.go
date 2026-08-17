package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config содержит всю конфигурацию приложения.
type Config struct {
	DatabaseURL      string
	TelegramBotToken string
	SpeechProvider   string
	LLMProvider      string

	WorkerConcurrency int

	SpeechTimeout   time.Duration
	LLMTimeout      time.Duration
	ShutdownTimeout time.Duration

	MockDelayMin time.Duration
	MockDelayMax time.Duration

	// GigaChat — параметры реального LLM-провайдера (опционально).
	
	GigaChatHost    string
	GigaChatAuthKey string
	GigaChatRqUID   string
	// GigaChatOAuthHost — хост OAuth-эндпоинта (получение токена).
	GigaChatOAuthHost string
	// GigaChatCaCert — путь к корневому сертификату.
	GigaChatCaCert string
}

// Load читает конфигурацию из файла .env и переменных окружения.
func Load() (Config, error) {
	// Загружаем .env, если он существует.
	_ = godotenv.Load()

	cfg := Config{
		DatabaseURL:      getEnv("DATABASE_URL", ""),
		TelegramBotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		SpeechProvider:   getEnv("SPEECH_PROVIDER", "mock"),
		LLMProvider:      getEnv("LLM_PROVIDER", "mock"),

		WorkerConcurrency: getEnvInt("WORKER_CONCURRENCY", 3),

		SpeechTimeout:   getEnvDuration("SPEECH_TIMEOUT", 30*time.Second),
		LLMTimeout:      getEnvDuration("LLM_TIMEOUT", 30*time.Second),
		ShutdownTimeout: getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),

		MockDelayMin: getEnvDuration("MOCK_DELAY_MIN", 2*time.Second),
		MockDelayMax: getEnvDuration("MOCK_DELAY_MAX", 5*time.Second),

		GigaChatHost:      getEnv("GIGACHAT_HOST", "https://gigachat.devices.sberbank.ru"),
		GigaChatAuthKey:   getEnv("GIGACHAT_AUTH_KEY", ""),
		GigaChatRqUID:     getEnv("GIGACHAT_RQUID", ""),
		GigaChatOAuthHost: getEnv("GIGACHAT_OAUTH_HOST", "https://ngw.devices.sberbank.ru:9443"),
		GigaChatCaCert:    getEnv("GIGACHAT_CA_CERT", ""),
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.TelegramBotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if c.WorkerConcurrency < 1 {
		return fmt.Errorf("WORKER_CONCURRENCY must be >= 1")
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
