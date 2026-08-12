// Package models содержит доменные модели приложения.
package models

import (
	"time"

	"github.com/google/uuid"
)

// User представляет зарегистрированного пользователя системы.
type User struct {
	ID         uuid.UUID
	ExternalID int64
	Username   string
	CreatedAt  time.Time
}

// Meeting представляет загруженную встречу/аудиофайл.
type Meeting struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Title     string
	FilePath  string
	FileSize  int64
	MimeType  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskStatus представляет статус обработки задачи.
type TaskStatus string

const (
	StatusCreated     TaskStatus = "created"
	StatusProcessing  TaskStatus = "processing"
	StatusTranscribed TaskStatus = "transcribed"
	StatusSummarized  TaskStatus = "summarized"
	StatusCompleted   TaskStatus = "completed"
	StatusFailed      TaskStatus = "failed"
)

// Task представляет задачу обработки встречи.
type Task struct {
	ID           uuid.UUID
	MeetingID    uuid.UUID
	Status       TaskStatus
	ErrorMessage string
	RetryCount   int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TaskEvent представляет событие изменения статуса в истории задачи.
type TaskEvent struct {
	ID        uuid.UUID
	TaskID    uuid.UUID
	Status    TaskStatus
	Message   string
	CreatedAt time.Time
}

// Transcript представляет транскрипцию встречи.
type Transcript struct {
	ID        uuid.UUID
	MeetingID uuid.UUID
	Text      string
	CreatedAt time.Time
}

// Summary представляет краткую выжимку встречи, сгенерированную LLM.
type Summary struct {
	ID        uuid.UUID
	MeetingID uuid.UUID
	Text      string
	CreatedAt time.Time
}

// ChatRole представляет роль отправителя сообщения в чате.
type ChatRole string

const (
	RoleUser      ChatRole = "user"
	RoleAssistant ChatRole = "assistant"
)

// ChatMessage представляет сообщение в истории чата по встрече.
type ChatMessage struct {
	ID        uuid.UUID
	MeetingID uuid.UUID
	UserID    uuid.UUID
	Role      ChatRole
	Message   string
	CreatedAt time.Time
}

// SearchResult представляет один результат поиска по встречам.
type SearchResult struct {
	MeetingID uuid.UUID
	CreatedAt time.Time
	Status    TaskStatus
	Snippet   string
}

// MeetingListItem — элемент списка встреч пользователя.

type MeetingListItem struct {
	MeetingID uuid.UUID
	Title     string
	CreatedAt time.Time
	Status    TaskStatus
	Summary   string
}
