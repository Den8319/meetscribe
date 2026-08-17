package llm

import (
	"github.com/Den8319/meetscribe/internal/config"
	"github.com/Den8319/meetscribe/internal/repository/opt"
)

// NewClient создаёт реализацию llm.Client по конфигурации.
// Переключение провайдера — одна строка в .env (LLM_PROVIDER=mock|gigachat).
func NewClient(cfg config.Config) Client {
	switch cfg.LLMProvider {
	case "mock":
		return NewMock(opt.WithDelay[MockClient](cfg.MockDelayMin, cfg.MockDelayMax))
	case "gigachat":
		// Реальная интеграция GigaChat
		return NewGigaChatClient(cfg.GigaChatHost, cfg.GigaChatOAuthHost, cfg.GigaChatAuthKey, cfg.GigaChatRqUID, cfg.GigaChatCaCert)
	default:
		// Неизвестный провайдер — безопасно переходим к mock.
		return NewMock(opt.WithDelay[MockClient](cfg.MockDelayMin, cfg.MockDelayMax))
	}
}
