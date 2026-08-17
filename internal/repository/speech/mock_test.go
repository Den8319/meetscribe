package speech

import (
	"context"
	"testing"
	"time"

	"github.com/Den8319/meetscribe/internal/repository/opt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFastMock создаёт mock с почти нулевой задержкой для быстрых тестов.
func newFastMock() *MockClient {
	return NewMock(
		opt.WithDelay[MockClient](time.Millisecond, time.Millisecond),
	)
}

// TestTranscribe_Deterministic проверяет, что одинаковое аудио даёт одинаковый сценарий.
func TestTranscribe_Deterministic(t *testing.T) {
	ctx := context.Background()
	m := newFastMock()

	audio := []byte("same audio bytes")

	first, err := m.Transcribe(ctx, audio, "audio/ogg")
	require.NoError(t, err)

	second, err := m.Transcribe(ctx, audio, "audio/ogg")
	require.NoError(t, err)

	assert.Equal(t, first, second, "same audio must map to same scenario")
	assert.NotEmpty(t, first)
}

// TestTranscribe_ScenarioPool проверяет, что все сценарии из пула достижимы.
func TestTranscribe_ScenarioPool(t *testing.T) {
	ctx := context.Background()
	m := newFastMock()

	seen := make(map[string]bool)
	// Прогоняем разные байты — должны наткнуться на разные сценарии.
	for i := range 64 {
		audio := []byte{byte(i), byte(i * 3), byte(i * 7)}
		text, err := m.Transcribe(ctx, audio, "audio/ogg")
		require.NoError(t, err)
		seen[text] = true
	}

	assert.GreaterOrEqual(t, len(seen), 2, "must reach at least 2 scenarios")
}

// TestTranscribe_ContextCancelled проверяет, что отмена контекста прерывает задержку.
func TestTranscribe_ContextCancelled(t *testing.T) {
	m := NewMock(opt.WithDelay[MockClient](10*time.Second, 10*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := m.Transcribe(ctx, []byte("audio"), "audio/ogg")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, time.Second, "must return immediately on cancelled context")
}

// TestTranscribe_TimeoutMidDelay проверяет прерывание по таймауту контекста.
func TestTranscribe_TimeoutMidDelay(t *testing.T) {
	m := NewMock(opt.WithDelay[MockClient](5*time.Second, 5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := m.Transcribe(ctx, []byte("audio"), "audio/ogg")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 2*time.Second, "must not wait full 5s delay")
}

// BenchmarkTranscribe измеряет пропускную способность mock Transcribe
// с нулевой задержкой. b.Loop() (Go 1.24+) даёт стабильнее результаты,
// чем ручной цикл for i := 0; i < b.N; i++ — рантайм сам управляет
// количеством итераций и временем прогона.
func BenchmarkTranscribe(b *testing.B) {
	m := NewMock(opt.WithDelay[MockClient](0, 0))
	ctx := context.Background()
	audio := []byte("benchmark audio sample")

	for b.Loop() {
		_, _ = m.Transcribe(ctx, audio, "audio/ogg")
	}
}
