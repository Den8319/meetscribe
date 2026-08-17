package speech

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/Den8319/meetscribe/internal/repository/opt"
)

// mockScenarios содержит 4 предзаписанных стенограммы для имитации распознавания.
var mockScenarios = []string{
	// Сценарий 0 — планёрка по проекту MeetScribe.
	`Иван: Всем привет, начинаем планёрку по проекту MeetScribe.
Мария: Я закончила настройку воркеров, теперь обрабатываем три встречи параллельно.
Иван: Отлично. Нужно ещё добавить полнотекстовый поиск по встречам.
Пётр: База данных готова, индексы созданы. Пятница — дедлайн.
Мария: Уточню у заказчика требования к выжимке. До встречи!`,

	// Сценарий 1 — интервью с кандидатом.
	`Алексей: Расскажите о вашем опыте работы с Go.
Кандидат: Три года пишу микросервисы, использую pgx и database/sql.
Алексей: Как подходите к тестированию?
Кандидат: Предпочитаю интеграционные тесты с реальной БД в Docker.
Алексей: Когда сможете выйти? Пятница устроит?
Кандидат: Да, в пятницу готов начать.`,

	// Сценарий 2 — созвон с клиентом.
	`Дмитрий: Добрый день! Обсудим требования к боту.
Клиент: Нам нужен Telegram-бот для конспектов встреч.
Дмитрий: Понял. Распознавание речи и выжимка — есть готовые mock-реализации.
Клиент: Сроки? Когда увидим демо?
Дмитрий: На следующей неделе, в пятницу покажу прототип.
Клиент: Отлично, договорились.`,

	// Сценарий 3 — технический разбор.
	`Сергей: Разбираем архитектуру. Service layer зависит от интерфейсов, не от реализаций.
Анна: Репозиторий на database/sql, миграции через goose.
Сергей: Воркер обрабатывает аудио в памяти, файлы не храним.
Анна: Тесты покрывают транзакции и изоляцию пользователей.
Сергей: Хорошо. В пятницу проводим ревью.`,
}

// MockClient — тестовая реализация speech.Client.
// Возвращает один из предзаписанных сценариев детерминированно (по хэшу аудио),
// имитирует задержку распознавания и уважает отмену контекста.
type MockClient struct {
	delayMin time.Duration
	delayMax time.Duration
}

// NewMock создаёт mock-клиент (generic option pattern).
// По умолчанию задержка 2-5 секунд; настраивается опцией opt.WithDelay.
func NewMock(opts ...opt.Option[MockClient]) *MockClient {
	m := &MockClient{
		delayMin: 2 * time.Second,
		delayMax: 5 * time.Second,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// SetDelay реализует opt.Delayer.
func (m *MockClient) SetDelay(min, max time.Duration) {
	m.delayMin = min
	m.delayMax = max
}

// Transcribe имитирует распознавание речи: выбирает сценарий по хэшу аудио,
// ждёт случайную задержку в пределах [delayMin, delayMax] и возвращает текст.
// При отмене контекста возвращает ошибку немедленно, не дожидаясь задержки.
func (m *MockClient) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error) {
	// Детерминированный выбор сценария по SHA-256(audio).
	// Одинаковый аудио → одинаковый сценарий, что удобно для тестов.
	hash := sha256.Sum256(audio)
	index := binary.BigEndian.Uint32(hash[:4]) % uint32(len(mockScenarios))
	scenario := mockScenarios[index]

	// Имитируем задержку распознавания речи.
	delay := m.delayMin
	if m.delayMax > m.delayMin {
		spread := m.delayMax - m.delayMin

		offset := time.Duration(binary.BigEndian.Uint32(hash[4:8])%uint32(spread)) % spread
		delay = m.delayMin + offset
	}

	select {
	case <-time.After(delay):
		return scenario, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
