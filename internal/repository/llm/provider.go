package llm

import (
	"time"

	"github.com/Den8319/meetscribe/internal/config"
)

// NewClient создаёт реализацию llm.Client по конфигурации.
// Переключение провайдера — одна строка в .env (LLM_PROVIDER=mock|gigachat).
func NewClient(cfg config.Config) Client {
	switch cfg.LLMProvider {
	case "mock":
		return NewMock(WithDelay(cfg.MockDelayMin, cfg.MockDelayMax))
	case "gigachat":
		// Реальная интеграция GigaChat
		return NewGigaChatClient(cfg.GigaChatHost, cfg.GigaChatOAuthHost, cfg.GigaChatAuthKey, cfg.GigaChatRqUID, cfg.GigaChatCaCert)
	default:
		// Неизвестный провайдер — безопасно переходим к mock.
		return NewMock(WithDelay(cfg.MockDelayMin, cfg.MockDelayMax))
	}
}

// Option — функциональная опция.
type Option[T any] func(*T)

// WithDelay задаёт диапазон задержки mock-клиента.
func WithDelay(min, max time.Duration) Option[MockClient] {
	return func(m *MockClient) {
		m.delayMin = min
		m.delayMax = max
	}
}
