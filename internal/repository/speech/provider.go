package speech

import (
	"github.com/Den8319/meetscribe/internal/config"
	"github.com/Den8319/meetscribe/internal/repository/opt"
)

// NewClient создаёт реализацию speech.Client по конфигурации.
// Переключение провайдера — одна строка в .env (SPEECH_PROVIDER=mock).
// Реальные провайдеры (Yandex SpeechKit, Whisper и т.д.) добавляются здесь же.
func NewClient(cfg config.Config) Client {
	switch cfg.SpeechProvider {
	case "mock":
		return NewMock(opt.WithDelay[MockClient](cfg.MockDelayMin, cfg.MockDelayMax))
	default:
		// Неизвестный провайдер — безопасно падаем на mock,
		// чтобы приложение продолжало работать в разработке.
		return NewMock(opt.WithDelay[MockClient](cfg.MockDelayMin, cfg.MockDelayMax))
	}
}
