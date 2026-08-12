package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFastMock создаёт mock с почти нулевой задержкой для быстрых тестов.
func newFastMock() *MockClient {
	return NewMock(
		WithDelay(time.Millisecond, time.Millisecond),
	)
}

// TestSummarize_ContainsSections проверяет структуру выжимки.
func TestSummarize_ContainsSections(t *testing.T) {
	ctx := context.Background()
	m := newFastMock()

	transcript := "Иван: Начинаем планёрку. Мария: Пятница — дедлайн по поиску."

	summary, err := m.Summarize(ctx, transcript)
	require.NoError(t, err)

	assert.Contains(t, summary, "Краткая выжимка встречи")
	assert.Contains(t, summary, "Иван")
	assert.Contains(t, summary, "Мария")
	assert.Contains(t, summary, "пятница")
}

// TestChat_KeywordMatching проверяет ответы на ключевые вопросы.
func TestChat_KeywordMatching(t *testing.T) {
	ctx := context.Background()
	m := newFastMock()

	contextText := "Иван: Всем привет. Мария: Закончила воркеры. Пятница — дедлайн."

	// Вопрос про участника.
	ans, err := m.Chat(ctx, contextText, nil, "Кто такой Иван?")
	require.NoError(t, err)
	assert.Contains(t, ans, "Иван")

	// Вопрос про сроки.
	ans, err = m.Chat(ctx, contextText, nil, "Когда дедлайн?")
	require.NoError(t, err)
	assert.Contains(t, ans, "пятница")

	// Неизвестный вопрос — вежливый ответ.
	ans, err = m.Chat(ctx, contextText, nil, "Какая погода в Москве?")
	require.NoError(t, err)
	assert.Contains(t, ans, "недостаточно информации")
}

// TestMock_ContextCancelled проверяет уважение к ctx: при отмене контекста
// метод возвращается быстро, не дожидаясь задержки (критерий Дня 3).
func TestMock_ContextCancelled(t *testing.T) {
	// Mock с большой задержкой — если ctx не уважается, тест зависнет.
	m := NewMock(WithDelay(10*time.Second, 10*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем сразу

	start := time.Now()
	_, err := m.Summarize(ctx, "текст")
	elapsed := time.Since(start)

	require.Error(t, err, "must return error on cancelled context")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, time.Second, "must return immediately, not wait for delay")
}

// TestMock_SummarizeRespectsContext проверяет, что при отмене посреди задержки
// метод тоже прерывается.
func TestMock_SummarizeRespectsContext(t *testing.T) {
	m := NewMock(WithDelay(5*time.Second, 5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := m.Summarize(ctx, "текст")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 2*time.Second, "must not wait full 5s delay")
}

// TestChat_IgnoresEmptyHistory проверяет, что Chat работает без истории.
func TestChat_IgnoresEmptyHistory(t *testing.T) {
	ctx := context.Background()
	m := newFastMock()

	ans, err := m.Chat(ctx, "текст встречи", nil, "Что обсуждали?")
	require.NoError(t, err)
	assert.NotEmpty(t, ans)
}

// TestBuildSummary_EmptyTranscript — деградация на пустом тексте.
func TestBuildSummary_EmptyTranscript(t *testing.T) {
	summary := buildSummary("")
	assert.Contains(t, summary, "Участники: не указаны")
	assert.Contains(t, summary, "Сроки: не указаны")
	assert.True(t, strings.Contains(summary, "Тема: не определена"))
}
