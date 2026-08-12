package speech

import (
	"time"

	"github.com/Den8319/meetscribe/internal/config"
)

// NewClient создаёт реализацию speech.Client по конфигурации.
// Переключение провайдера — одна строка в .env (SPEECH_PROVIDER=mock).
// Реальные провайдеры (Yandex SpeechKit, Whisper и т.д.) добавляются здесь же.
func NewClient(cfg config.Config) Client {
	switch cfg.SpeechProvider {
	case "mock":
		return NewMock(WithDelay(cfg.MockDelayMin, cfg.MockDelayMax))
	default:
		// Неизвестный провайдер — безопасно падаем на mock,
		// чтобы приложение продолжало работать в разработке.
		return NewMock(WithDelay(cfg.MockDelayMin, cfg.MockDelayMax))
	}
}

// Option — функциональная опция (generic option pattern, требование Go 1.26).
// Конструкторы принимают opts ...Option[T] и применяют их к *T.
type Option[T any] func(*T)

// WithDelay задаёт диапазон задержки mock-клиента.
func WithDelay(min, max time.Duration) Option[MockClient] {
	return func(m *MockClient) {
		m.delayMin = min
		m.delayMax = max
	}
}
