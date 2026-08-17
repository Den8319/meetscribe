// Package service содержит бизнес-логику приложения.
// Здесь же определён интерфейс Storage — его определяет главный потребитель
// (MeetingService), а реализацию предоставляет internal/repository/db.
package service

import (
	"context"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
)

// Storage — контракт хранилища, необходимый сервисному слою.
type Storage interface {
	//Пользователи
	CreateUser(ctx context.Context, externalID int64, username string) (models.User, error)
	GetUserByExternalID(ctx context.Context, externalID int64) (models.User, error)

	//Встречи
	CreateMeetingWithTask(ctx context.Context, userID uuid.UUID, title, filePath string, fileSize int64, mimeType, text string, audio []byte) (models.Meeting, error)
	GetMeetingByID(ctx context.Context, userID, meetingID uuid.UUID) (models.Meeting, error)
	GetMeetingInput(ctx context.Context, meetingID uuid.UUID) (models.MeetingInput, error)
	ListMeetingsByUser(ctx context.Context, userID uuid.UUID) ([]models.Meeting, error)
	ListMeetingsDetailed(ctx context.Context, userID uuid.UUID) ([]models.MeetingListItem, error)

	//Задачи
	GetTaskByMeetingID(ctx context.Context, meetingID uuid.UUID) (models.Task, error)
	UpdateTaskStatus(ctx context.Context, taskID uuid.UUID, status models.TaskStatus, msg string) error
	GetTasksByStatus(ctx context.Context, statuses ...models.TaskStatus) ([]models.Task, error)
	SaveError(ctx context.Context, taskID uuid.UUID, errMsg string) error

	//Транскрипции и выжимки
	SaveTranscript(ctx context.Context, meetingID uuid.UUID, text string) error
	GetTranscript(ctx context.Context, userID, meetingID uuid.UUID) (models.Transcript, error)
	SaveSummary(ctx context.Context, meetingID uuid.UUID, text string) error
	GetSummary(ctx context.Context, userID, meetingID uuid.UUID) (models.Summary, error)

	// --- Поиск ---
	SearchMeetings(ctx context.Context, userID uuid.UUID, query string) ([]models.SearchResult, error)

	// --- Чат ---
	SaveChatMessage(ctx context.Context, msg models.ChatMessage) error
	ListChatHistory(ctx context.Context, userID, meetingID uuid.UUID) ([]models.ChatMessage, error)
}
