package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
)

// MeetingService — бизнес-логика работы со встречами.
type MeetingService struct {
	storage Storage
	llm     LLMClient
}

// LLMClient — контракт языковой модели, необходимый сервису.
type LLMClient interface {
	Summarize(ctx context.Context, transcript string) (string, error)
	Chat(ctx context.Context, contextText string, history []models.ChatMessage, question string) (string, error)
}

// NewMeetingService создаёт сервис с указанными зависимостями.
func NewMeetingService(storage Storage, llm LLMClient) *MeetingService {
	return &MeetingService{storage: storage, llm: llm}
}

// RegisterUser находит пользователя по externalID или создаёт нового.
func (s *MeetingService) RegisterUser(ctx context.Context, externalID int64, username string) (models.User, error) {
	user, err := s.storage.GetUserByExternalID(ctx, externalID)
	if err == nil {
		return user, nil
	}
	// Ошибка "not found" — создаём; любая другая — возвращаем.
	if !models.IsNotFound(err) {
		return models.User{}, fmt.Errorf("get user: %w", err)
	}
	user, err = s.storage.CreateUser(ctx, externalID, username)
	if err != nil {
		slog.Error("user registration failed", "external_id", externalID, "error", err)
		return models.User{}, fmt.Errorf("create user: %w", err)
	}
	slog.Info("user registered", "user_id", user.ID, "external_id", externalID, "username", username)
	return user, nil
}

// UploadAndStartProcessing создаёт встречу и задачу на обработку.
// Входные данные (текст или аудио) сохраняются в БД вместе со встречей —
// воркер читает их оттуда, что гарантирует восстановление после рестарта.
func (s *MeetingService) UploadAndStartProcessing(
	ctx context.Context,
	userID uuid.UUID,
	title string,
	audio []byte,
	mimeType string,
	text string,
) (models.Meeting, error) {
	meeting, err := s.storage.CreateMeetingWithTask(ctx, userID, title, "", int64(len(audio)), mimeType, text, audio)
	if err != nil {
		slog.Error("meeting creation failed", "user_id", userID, "title", title, "error", err)
		return models.Meeting{}, fmt.Errorf("create meeting: %w", err)
	}

	inputType := "audio"
	if text != "" {
		inputType = "text"
	}
	slog.Info("meeting uploaded",
		"meeting_id", meeting.ID,
		"user_id", userID,
		"title", title,
		"input_type", inputType,
		"file_size", len(audio),
		"mime_type", mimeType,
	)
	return meeting, nil
}

// GetMeetingStatus возвращает статус обработки встречи (с проверкой прав).
func (s *MeetingService) GetMeetingStatus(ctx context.Context, userID, meetingID uuid.UUID) (models.Task, error) {
	meeting, err := s.storage.GetMeetingByID(ctx, userID, meetingID)
	if err != nil {
		return models.Task{}, err
	}
	return s.storage.GetTaskByMeetingID(ctx, meeting.ID)
}

// GetTranscript возвращает транскрипцию встречи (с проверкой прав).
func (s *MeetingService) GetTranscript(ctx context.Context, userID, meetingID uuid.UUID) (models.Transcript, error) {
	return s.storage.GetTranscript(ctx, userID, meetingID)
}

// GetSummary возвращает выжимку встречи (с проверкой прав).
func (s *MeetingService) GetSummary(ctx context.Context, userID, meetingID uuid.UUID) (models.Summary, error) {
	return s.storage.GetSummary(ctx, userID, meetingID)
}

// ListMeetings возвращает список встреч пользователя со статусом и выжимкой
func (s *MeetingService) ListMeetings(ctx context.Context, userID uuid.UUID) ([]models.MeetingListItem, error) {
	return s.storage.ListMeetingsDetailed(ctx, userID)
}

// Search ищет встречи пользователя по ключевому слову.
func (s *MeetingService) Search(ctx context.Context, userID uuid.UUID, query string) ([]models.SearchResult, error) {
	return s.storage.SearchMeetings(ctx, userID, query)
}

// AskQuestion задаёт вопрос по контексту встречи и сохраняет диалог.
// Загружает историю чата, вызывает LLM.Chat, сохраняет вопрос и ответ.
func (s *MeetingService) AskQuestion(ctx context.Context, userID, meetingID uuid.UUID, question string) (string, error) {
	// Проверка прав: чужая встреча → ErrNotFound.
	meeting, err := s.storage.GetMeetingByID(ctx, userID, meetingID)
	if err != nil {
		return "", err
	}

	// Контекст для LLM: транскрипция (или выжимка, если транскрипции нет).
	contextText := ""
	if tr, err := s.storage.GetTranscript(ctx, userID, meeting.ID); err == nil {
		contextText = tr.Text
	} else if sm, err := s.storage.GetSummary(ctx, userID, meeting.ID); err == nil {
		contextText = sm.Text
	} else {
		return "", models.ErrNotProcessed
	}

	history, err := s.storage.ListChatHistory(ctx, userID, meeting.ID)
	if err != nil {
		return "", fmt.Errorf("list chat history: %w", err)
	}

	slog.Info("llm chat requested", "meeting_id", meeting.ID, "user_id", userID, "history_len", len(history))
	answer, err := s.llm.Chat(ctx, contextText, history, question)
	if err != nil {
		slog.Error("llm chat failed", "meeting_id", meeting.ID, "user_id", userID, "error", err)
		return "", fmt.Errorf("llm chat: %w", err)
	}

	// Сохраняем вопрос и ответ двумя записями в истории.
	if err := s.storage.SaveChatMessage(ctx, models.ChatMessage{
		MeetingID: meeting.ID, UserID: userID, Role: models.RoleUser, Message: question,
	}); err != nil {
		return "", fmt.Errorf("save question: %w", err)
	}
	if err := s.storage.SaveChatMessage(ctx, models.ChatMessage{
		MeetingID: meeting.ID, UserID: userID, Role: models.RoleAssistant, Message: answer,
	}); err != nil {
		return "", fmt.Errorf("save answer: %w", err)
	}

	return answer, nil
}
