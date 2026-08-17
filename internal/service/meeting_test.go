package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLLMForService — LLM-мок для тестов MeetingService.
// Возвращает предсказуемые ответы и считает вызовы.
type fakeLLMForService struct {
	chatAnswer    string
	chatErr       error
	chatCalls     int
	summarizeCall int
}

func (f *fakeLLMForService) Summarize(ctx context.Context, transcript string) (string, error) {
	f.summarizeCall++
	return "summary", nil
}

func (f *fakeLLMForService) Chat(ctx context.Context, contextText string, history []models.ChatMessage, question string) (string, error) {
	f.chatCalls++
	if f.chatErr != nil {
		return "", f.chatErr
	}
	return f.chatAnswer, nil
}

// --- Тесты ---

// TestRegisterUser_CreatesNew — первый вызов создаёт пользователя.
func TestRegisterUser_CreatesNew(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	user, err := svc.RegisterUser(context.Background(), 12345, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(12345), user.ExternalID)
	assert.Equal(t, "alice", user.Username)
	assert.NotEqual(t, uuid.Nil, user.ID)
}

// TestRegisterUser_ReturnsExisting — повторный вызов возвращает того же пользователя.
func TestRegisterUser_ReturnsExisting(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	user1, err := svc.RegisterUser(context.Background(), 12345, "alice")
	require.NoError(t, err)

	user2, err := svc.RegisterUser(context.Background(), 12345, "alice_updated")
	require.NoError(t, err)

	assert.Equal(t, user1.ID, user2.ID, "should return same user ID")
}

// TestUploadAndStartProcessing_CreatesMeetingAndTask — загрузка создаёт встречу и задачу.
func TestUploadAndStartProcessing_CreatesMeetingAndTask(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	user, _ := svc.RegisterUser(context.Background(), 12345, "alice")

	meeting, err := svc.UploadAndStartProcessing(context.Background(), user.ID, "Test Meeting", []byte("audio"), "audio/ogg", "")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, meeting.ID)
	assert.Equal(t, user.ID, meeting.UserID)
	assert.Equal(t, "Test Meeting", meeting.Title)

	// Проверяем, что задача создана.
	task, err := storage.GetTaskByMeetingID(context.Background(), meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCreated, task.Status)
}

// TestGetMeetingStatus_ForbiddenForOtherUser — чужая встреча → ErrNotFound.
func TestGetMeetingStatus_ForbiddenForOtherUser(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	userA, _ := svc.RegisterUser(context.Background(), 111, "alice")
	userB, _ := svc.RegisterUser(context.Background(), 222, "bob")

	meeting, err := svc.UploadAndStartProcessing(context.Background(), userA.ID, "Secret", []byte("audio"), "audio/ogg", "")
	require.NoError(t, err)

	// User B пытается получить статус встречи User A → ErrNotFound.
	_, err = svc.GetMeetingStatus(context.Background(), userB.ID, meeting.ID)
	assert.True(t, errors.Is(err, models.ErrNotFound))
}

// TestGetTranscript_ForbiddenForOtherUser — чужая транскрипция недоступна.
func TestGetTranscript_ForbiddenForOtherUser(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	userA, _ := svc.RegisterUser(context.Background(), 111, "alice")
	userB, _ := svc.RegisterUser(context.Background(), 222, "bob")

	meeting, err := svc.UploadAndStartProcessing(context.Background(), userA.ID, "Secret", []byte("audio"), "audio/ogg", "")
	require.NoError(t, err)

	_, err = svc.GetTranscript(context.Background(), userB.ID, meeting.ID)
	assert.True(t, errors.Is(err, models.ErrNotFound))
}

// TestListMeetings_OnlyOwnMeetings — список содержит только свои встречи.
func TestListMeetings_OnlyOwnMeetings(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	userA, _ := svc.RegisterUser(context.Background(), 111, "alice")
	userB, _ := svc.RegisterUser(context.Background(), 222, "bob")

	_, _ = svc.UploadAndStartProcessing(context.Background(), userA.ID, "Meeting A1", []byte("a"), "audio/ogg", "")
	_, _ = svc.UploadAndStartProcessing(context.Background(), userA.ID, "Meeting A2", []byte("b"), "audio/ogg", "")
	_, _ = svc.UploadAndStartProcessing(context.Background(), userB.ID, "Meeting B1", []byte("c"), "audio/ogg", "")

	items, err := svc.ListMeetings(context.Background(), userA.ID)
	require.NoError(t, err)
	assert.Len(t, items, 2, "user A should see only 2 meetings")
}

// TestAskQuestion_SavesHistory — вопрос и ответ сохраняются в историю.
func TestAskQuestion_SavesHistory(t *testing.T) {
	storage := newFakeStorage()
	llm := &fakeLLMForService{chatAnswer: "ответ бота"}
	svc := NewMeetingService(storage, llm)

	user, _ := svc.RegisterUser(context.Background(), 111, "alice")
	meeting, err := svc.UploadAndStartProcessing(context.Background(), user.ID, "Test", []byte("audio"), "audio/ogg", "")
	require.NoError(t, err)

	// Сохраняем транскрипцию (нужна для контекста LLM).
	require.NoError(t, storage.SaveTranscript(context.Background(), meeting.ID, "текст встречи"))

	answer, err := svc.AskQuestion(context.Background(), user.ID, meeting.ID, "что обсуждали?")
	require.NoError(t, err)
	assert.Equal(t, "ответ бота", answer)
	assert.Equal(t, 1, llm.chatCalls, "LLM.Chat should be called once")

	// Проверяем, что история сохранилась (2 сообщения: вопрос + ответ).
	history, err := storage.ListChatHistory(context.Background(), user.ID, meeting.ID)
	require.NoError(t, err)
	assert.Len(t, history, 2, "should save question + answer")
	assert.Equal(t, models.RoleUser, history[0].Role)
	assert.Equal(t, "что обсуждали?", history[0].Message)
	assert.Equal(t, models.RoleAssistant, history[1].Role)
	assert.Equal(t, "ответ бота", history[1].Message)
}

// TestAskQuestion_NotProcessed — нет транскрипции и выжимки → ErrNotProcessed.
func TestAskQuestion_NotProcessed(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	user, _ := svc.RegisterUser(context.Background(), 111, "alice")
	meeting, err := svc.UploadAndStartProcessing(context.Background(), user.ID, "Test", []byte("audio"), "audio/ogg", "")
	require.NoError(t, err)

	_, err = svc.AskQuestion(context.Background(), user.ID, meeting.ID, "вопрос")
	assert.True(t, errors.Is(err, models.ErrNotProcessed))
}

// TestAskQuestion_ForbiddenForOtherUser — чужая встреча → ErrNotFound.
func TestAskQuestion_ForbiddenForOtherUser(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	userA, _ := svc.RegisterUser(context.Background(), 111, "alice")
	userB, _ := svc.RegisterUser(context.Background(), 222, "bob")

	meeting, err := svc.UploadAndStartProcessing(context.Background(), userA.ID, "Secret", []byte("audio"), "audio/ogg", "")
	require.NoError(t, err)

	_, err = svc.AskQuestion(context.Background(), userB.ID, meeting.ID, "вопрос")
	assert.True(t, errors.Is(err, models.ErrNotFound))
}

// TestAskQuestion_LLMError — ошибка LLM возвращается.
func TestAskQuestion_LLMError(t *testing.T) {
	storage := newFakeStorage()
	llm := &fakeLLMForService{chatErr: errors.New("llm unavailable")}
	svc := NewMeetingService(storage, llm)

	user, _ := svc.RegisterUser(context.Background(), 111, "alice")
	meeting, err := svc.UploadAndStartProcessing(context.Background(), user.ID, "Test", []byte("audio"), "audio/ogg", "")
	require.NoError(t, err)
	require.NoError(t, storage.SaveTranscript(context.Background(), meeting.ID, "текст"))

	_, err = svc.AskQuestion(context.Background(), user.ID, meeting.ID, "вопрос")
	assert.Error(t, err)
}

// TestSearch_OnlyOwnMeetings — поиск изолирован по пользователю.
func TestSearch_OnlyOwnMeetings(t *testing.T) {
	storage := newFakeStorage()
	svc := NewMeetingService(storage, &fakeLLMForService{})

	userA, _ := svc.RegisterUser(context.Background(), 111, "alice")
	userB, _ := svc.RegisterUser(context.Background(), 222, "bob")

	meetingA, _ := svc.UploadAndStartProcessing(context.Background(), userA.ID, "A", []byte("a"), "audio/ogg", "")
	meetingB, _ := svc.UploadAndStartProcessing(context.Background(), userB.ID, "B", []byte("b"), "audio/ogg", "")

	require.NoError(t, storage.SaveTranscript(context.Background(), meetingA.ID, "обсуждение проект"))
	require.NoError(t, storage.SaveTranscript(context.Background(), meetingB.ID, "обсуждение проект"))

	// User A ищет — должен найти только свою встречу.
	results, err := svc.Search(context.Background(), userA.ID, "проект")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, meetingA.ID, results[0].MeetingID)
}
