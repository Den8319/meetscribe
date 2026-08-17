package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Моки (in-memory, без реальной БД и API) ---

// fakeStorage — мини-реализация Storage для тестов воркера.
type fakeStorage struct {
	mu          sync.Mutex
	meetings    map[uuid.UUID]models.Meeting
	tasks       map[uuid.UUID]models.Task
	transcripts map[uuid.UUID]string
	summaries   map[uuid.UUID]string
	users       map[int64]models.User
	chatHistory map[uuid.UUID][]models.ChatMessage
	inputs      map[uuid.UUID]models.MeetingInput // входные данные встреч
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		meetings:    make(map[uuid.UUID]models.Meeting),
		tasks:       make(map[uuid.UUID]models.Task),
		transcripts: make(map[uuid.UUID]string),
		summaries:   make(map[uuid.UUID]string),
		users:       make(map[int64]models.User),
		chatHistory: make(map[uuid.UUID][]models.ChatMessage),
		inputs:      make(map[uuid.UUID]models.MeetingInput),
	}
}

func (f *fakeStorage) CreateMeetingWithTask(ctx context.Context, userID uuid.UUID, title, filePath string, fileSize int64, mimeType, text string, audio []byte) (models.Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := models.Meeting{ID: uuid.New(), UserID: userID, Title: title, MimeType: mimeType, FileSize: fileSize}
	f.meetings[m.ID] = m
	f.tasks[m.ID] = models.Task{ID: uuid.New(), MeetingID: m.ID, Status: models.StatusCreated}

	inputType := "audio"
	if text != "" {
		inputType = "text"
	}
	f.inputs[m.ID] = models.MeetingInput{
		MeetingID: m.ID,
		Type:      inputType,
		Text:      text,
		Audio:     audio,
		MimeType:  mimeType,
	}
	return m, nil
}

func (f *fakeStorage) GetMeetingInput(ctx context.Context, meetingID uuid.UUID) (models.MeetingInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.inputs[meetingID]
	if !ok {
		return models.MeetingInput{}, models.ErrNotFound
	}
	return in, nil
}

func (f *fakeStorage) GetMeetingByID(ctx context.Context, userID, meetingID uuid.UUID) (models.Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.meetings[meetingID]
	if !ok || m.UserID != userID {
		return models.Meeting{}, models.ErrNotFound
	}
	return m, nil
}

func (f *fakeStorage) ListMeetingsByUser(ctx context.Context, userID uuid.UUID) ([]models.Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Meeting
	for _, m := range f.meetings {
		if m.UserID == userID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStorage) ListMeetingsDetailed(ctx context.Context, userID uuid.UUID) ([]models.MeetingListItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.MeetingListItem
	for _, m := range f.meetings {
		if m.UserID != userID {
			continue
		}
		item := models.MeetingListItem{
			MeetingID: m.ID,
			Title:     m.Title,
			CreatedAt: m.CreatedAt,
			Status:    models.StatusCreated,
		}
		if t, ok := f.tasks[m.ID]; ok {
			item.Status = t.Status
		}
		if s, ok := f.summaries[m.ID]; ok {
			item.Summary = s
		}
		out = append(out, item)
	}
	return out, nil
}

func (f *fakeStorage) GetTaskByMeetingID(ctx context.Context, meetingID uuid.UUID) (models.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[meetingID]
	if !ok {
		return models.Task{}, models.ErrNotFound
	}
	return t, nil
}

func (f *fakeStorage) UpdateTaskStatus(ctx context.Context, taskID uuid.UUID, status models.TaskStatus, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tasks {
		if t.ID == taskID {
			t.Status = status
			f.tasks[t.MeetingID] = t
		}
	}
	return nil
}

func (f *fakeStorage) GetTasksByStatus(ctx context.Context, statuses ...models.TaskStatus) ([]models.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[models.TaskStatus]bool{}
	for _, s := range statuses {
		want[s] = true
	}
	var out []models.Task
	for _, t := range f.tasks {
		if want[t.Status] {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeStorage) SaveError(ctx context.Context, taskID uuid.UUID, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tasks {
		if t.ID == taskID {
			t.Status = models.StatusFailed
			t.ErrorMessage = errMsg
			f.tasks[t.MeetingID] = t
		}
	}
	return nil
}

func (f *fakeStorage) SaveTranscript(ctx context.Context, meetingID uuid.UUID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transcripts[meetingID] = text
	if t, ok := f.tasks[meetingID]; ok {
		t.Status = models.StatusTranscribed
		f.tasks[meetingID] = t
	}
	return nil
}

func (f *fakeStorage) SaveSummary(ctx context.Context, meetingID uuid.UUID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries[meetingID] = text
	if t, ok := f.tasks[meetingID]; ok {
		t.Status = models.StatusCompleted
		f.tasks[meetingID] = t
	}
	return nil
}

func (f *fakeStorage) GetTranscript(ctx context.Context, userID, meetingID uuid.UUID) (models.Transcript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.meetings[meetingID]
	if !ok || m.UserID != userID {
		return models.Transcript{}, models.ErrNotFound
	}
	text, hasTranscript := f.transcripts[meetingID]
	if !hasTranscript {
		return models.Transcript{}, models.ErrNotFound
	}
	return models.Transcript{MeetingID: meetingID, Text: text}, nil
}

func (f *fakeStorage) GetSummary(ctx context.Context, userID, meetingID uuid.UUID) (models.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.meetings[meetingID]
	if !ok || m.UserID != userID {
		return models.Summary{}, models.ErrNotFound
	}
	text, hasSummary := f.summaries[meetingID]
	if !hasSummary {
		return models.Summary{}, models.ErrNotFound
	}
	return models.Summary{MeetingID: meetingID, Text: text}, nil
}

func (f *fakeStorage) SearchMeetings(ctx context.Context, userID uuid.UUID, query string) ([]models.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.SearchResult
	for _, m := range f.meetings {
		if m.UserID != userID {
			continue
		}
		text := f.transcripts[m.ID] + " " + f.summaries[m.ID]
		if text == "" || !strings.Contains(strings.ToLower(text), strings.ToLower(query)) {
			continue
		}
		status := models.StatusCreated
		if t, ok := f.tasks[m.ID]; ok {
			status = t.Status
		}
		out = append(out, models.SearchResult{
			MeetingID: m.ID,
			CreatedAt: m.CreatedAt,
			Status:    status,
			Snippet:   text,
		})
	}
	return out, nil
}

func (f *fakeStorage) SaveChatMessage(ctx context.Context, msg models.ChatMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatHistory[msg.MeetingID] = append(f.chatHistory[msg.MeetingID], msg)
	return nil
}

func (f *fakeStorage) ListChatHistory(ctx context.Context, userID, meetingID uuid.UUID) ([]models.ChatMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.meetings[meetingID]; !ok {
		return nil, models.ErrNotFound
	}
	return f.chatHistory[meetingID], nil
}

func (f *fakeStorage) CreateUser(ctx context.Context, externalID int64, username string) (models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[externalID]; ok {
		return u, nil
	}
	u := models.User{ID: uuid.New(), ExternalID: externalID, Username: username}
	f.users[externalID] = u
	return u, nil
}

func (f *fakeStorage) GetUserByExternalID(ctx context.Context, externalID int64) (models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[externalID]
	if !ok {
		return models.User{}, models.ErrNotFound
	}
	return u, nil
}

// fakeSpeech — имитация speech-клиента с задержкой и счётчиком параллельных вызовов.
type fakeSpeech struct {
	mu         sync.Mutex
	delay      time.Duration
	concurrent int
	maxSeen    int
	active     int
	fail       bool
}

func (f *fakeSpeech) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxSeen {
		f.maxSeen = f.active
	}
	f.mu.Unlock()

	if f.fail {
		time.Sleep(f.delay)
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		return "", errors.New("speech provider error")
	}

	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		return "", ctx.Err()
	}

	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return "транскрипция встречи", nil
}

// fakeLLM — имитация LLM с задержкой.
type fakeLLM struct {
	delay time.Duration
	fail  bool
}

func (f *fakeLLM) Summarize(ctx context.Context, transcript string) (string, error) {
	if f.fail {
		time.Sleep(f.delay)
		return "", errors.New("llm provider error")
	}
	select {
	case <-time.After(f.delay):
		return "краткая выжимка", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeLLM) Chat(ctx context.Context, contextText string, history []models.ChatMessage, question string) (string, error) {
	return "ответ", nil
}

// --- Тесты ---

// TestWorkerPool_ConcurrencyLimit — критерий Дня 4:
// 10 задач при лимите 2, все завершаются, максимум 2 одновременно.
func TestWorkerPool_ConcurrencyLimit(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 50 * time.Millisecond}
	llm := &fakeLLM{delay: 50 * time.Millisecond}

	// Создаём 10 встреч (задач).
	meetings := make([]uuid.UUID, 0, 10)
	for range 10 {
		m, err := storage.CreateMeetingWithTask(context.Background(), uuid.New(), "test", "", 100, "audio/ogg", "", []byte("audio"))
		require.NoError(t, err)
		meetings = append(meetings, m.ID)
	}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp := NewWorkerPool(storage, sp, llm, 2, 10*time.Second, 10*time.Second, 10)
		wp.Start(ctx)

		// Отправляем 10 задач в очередь — только ссылка, аудио в БД.
		for _, id := range meetings {
			require.True(t, wp.Submit(Job{MeetingID: id}))
		}

		// time.Sleep продвигает виртуальное время: воркеры обрабатывают задачи.
		// 10 задач × 100мс (speech+LLM) / 2 воркера = 500мс; спим 2с с запасом.
		time.Sleep(2 * time.Second)

		// Останавливаем пул.
		cancel()
		wp.Stop()
		_ = wp.Wait()
	})

	// Лимит параллелизма: не более 2 одновременных speech-вызовов.
	assert.LessOrEqual(t, sp.maxSeen, 2, "max concurrent speech calls must be <= 2")

	// Все транскрипции и выжимки сохранены.
	storage.mu.Lock()
	defer storage.mu.Unlock()
	for _, id := range meetings {
		assert.Equal(t, models.StatusCompleted, storage.tasks[id].Status, "task must complete")
	}
	assert.Len(t, storage.transcripts, 10)
	assert.Len(t, storage.summaries, 10)
}

// TestWorkerPool_TextInput — текстовый ввод: speech не вызывается, транскрипция = текст.
func TestWorkerPool_TextInput(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 10 * time.Millisecond}
	llm := &fakeLLM{delay: 10 * time.Millisecond}

	m, err := storage.CreateMeetingWithTask(context.Background(), uuid.New(), "текстовая встреча", "", 0, "", "Иван: привет. Пятница — дедлайн.", nil)
	require.NoError(t, err)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp := NewWorkerPool(storage, sp, llm, 1, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)

		require.True(t, wp.Submit(Job{MeetingID: m.ID}))

		time.Sleep(500 * time.Millisecond)

		cancel()
		wp.Stop()
		_ = wp.Wait()
	})

	storage.mu.Lock()
	defer storage.mu.Unlock()
	assert.Equal(t, models.StatusCompleted, storage.tasks[m.ID].Status)
	assert.Equal(t, "Иван: привет. Пятница — дедлайн.", storage.transcripts[m.ID])
	assert.Equal(t, "краткая выжимка", storage.summaries[m.ID])
	assert.Zero(t, sp.maxSeen, "speech must not be called for text input")
}

// TestWorkerPool_SpeechFailure — ошибка speech → задача failed.
func TestWorkerPool_SpeechFailure(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 10 * time.Millisecond, fail: true}
	llm := &fakeLLM{delay: 10 * time.Millisecond}

	m, err := storage.CreateMeetingWithTask(context.Background(), uuid.New(), "test", "", 100, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp := NewWorkerPool(storage, sp, llm, 1, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)

		require.True(t, wp.Submit(Job{MeetingID: m.ID}))

		time.Sleep(500 * time.Millisecond)

		cancel()
		wp.Stop()
		_ = wp.Wait()
	})

	storage.mu.Lock()
	defer storage.mu.Unlock()
	assert.Equal(t, models.StatusFailed, storage.tasks[m.ID].Status)
	assert.Contains(t, storage.tasks[m.ID].ErrorMessage, "speech provider error")
}

// TestWorkerPool_AudioRestoredFromDB — после рестарта аудио берётся из БД,
// задача успешно обрабатывается (раньше audio=nil → failed).
func TestWorkerPool_AudioRestoredFromDB(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 10 * time.Millisecond}
	llm := &fakeLLM{delay: 10 * time.Millisecond}

	m, err := storage.CreateMeetingWithTask(context.Background(), uuid.New(), "test", "", 100, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp := NewWorkerPool(storage, sp, llm, 1, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)

		// Имитация восстановления: в очереди только MeetingID, без аудио.
		// Воркер читает аудио из БД → задача завершается успешно.
		require.True(t, wp.Submit(Job{MeetingID: m.ID}))

		time.Sleep(500 * time.Millisecond)

		cancel()
		wp.Stop()
		_ = wp.Wait()
	})

	storage.mu.Lock()
	defer storage.mu.Unlock()
	assert.Equal(t, models.StatusCompleted, storage.tasks[m.ID].Status, "task must complete — audio restored from db")
	assert.Equal(t, "краткая выжимка", storage.summaries[m.ID])
}

// TestWorkerPool_ContextCancellation — отмена контекста останавливает воркеры.
func TestWorkerPool_ContextCancellation(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 100 * time.Millisecond}
	llm := &fakeLLM{delay: 100 * time.Millisecond}

	var wp *WorkerPool

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp = NewWorkerPool(storage, sp, llm, 2, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)

		// Отменяем контекст → воркеры должны остановиться.
		cancel()
		// Wait ждёт, пока все горутины durably blocked (воркеры вышли по ctx.Done).
		synctest.Wait()
		_ = wp.Wait()
	})

	// synctest.Test вернулся — все горутины вышли, wp.Wait() уже завершён.
	_ = wp
}

// TestWorkerPool_StopClosesQueue — Stop закрывает очередь, Wait возвращается.
func TestWorkerPool_StopClosesQueue(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 10 * time.Millisecond}
	llm := &fakeLLM{delay: 10 * time.Millisecond}

	var wp *WorkerPool

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		wp = NewWorkerPool(storage, sp, llm, 1, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)
		wp.Stop() // закрываем очередь

		// Wait ждёт, пока воркеры выйдут по закрытому каналу.
		synctest.Wait()
		_ = wp.Wait()
	})

	_ = wp // synctest.Test вернулся — все горутины вышли
}

// TestWorkerPool_Recover — Recover находит незавершённые задачи.
func TestWorkerPool_Recover(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 10 * time.Millisecond}
	llm := &fakeLLM{delay: 10 * time.Millisecond}

	// Две задачи: одна created (с аудио в БД), одна completed.
	m1, err := storage.CreateMeetingWithTask(context.Background(), uuid.New(), "1", "", 100, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)
	m2, err := storage.CreateMeetingWithTask(context.Background(), uuid.New(), "2", "", 100, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	storage.mu.Lock()
	storage.tasks[m1.ID] = models.Task{ID: storage.tasks[m1.ID].ID, MeetingID: m1.ID, Status: models.StatusCreated}
	storage.tasks[m2.ID] = models.Task{ID: storage.tasks[m2.ID].ID, MeetingID: m2.ID, Status: models.StatusCompleted}
	storage.mu.Unlock()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp := NewWorkerPool(storage, sp, llm, 1, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)

		require.NoError(t, wp.Recover(ctx))

		// created-задача → в очереди → аудио из БД → completed.
		time.Sleep(500 * time.Millisecond)

		cancel()
		wp.Stop()
		_ = wp.Wait()
	})

	storage.mu.Lock()
	defer storage.mu.Unlock()
	assert.Equal(t, models.StatusCompleted, storage.tasks[m1.ID].Status, "created task must be recovered and completed")
	assert.Equal(t, models.StatusCompleted, storage.tasks[m2.ID].Status, "completed task must not be re-processed")
}

// TestWorkerPool_SubmitAfterStop — Submit после Stop не паникует
// (защита от send on closed channel при гонке shutdown ↔ Telegram-хендлер).
func TestWorkerPool_SubmitAfterStop(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 10 * time.Millisecond}
	llm := &fakeLLM{delay: 10 * time.Millisecond}

	m, err := storage.CreateMeetingWithTask(context.Background(), uuid.New(), "test", "", 100, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp := NewWorkerPool(storage, sp, llm, 1, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)

		// Останавливаем пул — очередь закрыта.
		wp.Stop()
		cancel()

		// Submit после Stop: должен вернуть false, не паникуя.
		require.False(t, wp.Submit(Job{MeetingID: m.ID}))

		// Повторный Stop безопасен (идемпотентен).
		wp.Stop()

		_ = wp.Wait()
	})
}

// TestWorkerPool_StopIdempotent — двойной Stop не паникует (close of closed channel).
func TestWorkerPool_StopIdempotent(t *testing.T) {
	storage := newFakeStorage()
	sp := &fakeSpeech{delay: 10 * time.Millisecond}
	llm := &fakeLLM{delay: 10 * time.Millisecond}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wp := NewWorkerPool(storage, sp, llm, 1, 10*time.Second, 10*time.Second, 4)
		wp.Start(ctx)

		wp.Stop()
		wp.Stop() // второй раз — не паникует
		cancel()

		_ = wp.Wait()
	})
}
