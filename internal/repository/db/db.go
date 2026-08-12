// Package db предоставляет подключение к PostgreSQL, миграции и реализацию Storage.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" 
	"github.com/pressly/goose/v3"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
)

// migrateDir — путь к папке с SQL-миграциями goose (на уровне проекта).
const migrateDir = "migrations"

// Connect открывает соединение с PostgreSQL через database/sql и применяет миграции.
func Connect(ctx context.Context, databaseURL string) (*sql.DB, error) {
	// Открываем соединение через стандартный database/sql.
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return sqlDB, nil
}

func runMigrations(sqlDB *sql.DB) error {
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	if err := goose.Up(sqlDB, migrateDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	return nil
}

// Repository — реализация Storage поверх *sql.DB.
type Repository struct {
	db *sql.DB
}

// New создаёт репозиторий на основе пула соединений *sql.DB.
func New(sqlDB *sql.DB) *Repository {
	return &Repository{db: sqlDB}
}

// --- Пользователи ---

// CreateUser создаёт пользователя или обновляет username при повторном входе
func (r *Repository) CreateUser(ctx context.Context, externalID int64, username string) (models.User, error) {
	const q = `
		INSERT INTO users (external_id, username)
		VALUES ($1, $2)
		ON CONFLICT (external_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id, external_id, username, created_at`

	var u models.User
	var id string
	err := r.db.QueryRowContext(ctx, q, externalID, username).
		Scan(&id, &u.ExternalID, &u.Username, &u.CreatedAt)
	if err != nil {
		return models.User{}, fmt.Errorf("create user: %w", err)
	}
	u.ID, err = uuid.Parse(id)
	if err != nil {
		return models.User{}, fmt.Errorf("parse user id: %w", err)
	}
	return u, nil
}

// GetUserByExternalID возвращает пользователя по внешнему идентификатору.
func (r *Repository) GetUserByExternalID(ctx context.Context, externalID int64) (models.User, error) {
	const q = `
		SELECT id, external_id, username, created_at
		FROM users
		WHERE external_id = $1`

	return scanUser(r.db.QueryRowContext(ctx, q, externalID))
}

// --- Встречи ---

// GetMeetingByID возвращает встречу, только если она принадлежит userID.
func (r *Repository) GetMeetingByID(ctx context.Context, userID, meetingID uuid.UUID) (models.Meeting, error) {
	const q = `
		SELECT id, user_id, title, file_path, file_size, mime_type, created_at, updated_at
		FROM meetings
		WHERE id = $1 AND user_id = $2`

	return scanMeeting(r.db.QueryRowContext(ctx, q, meetingID, userID))
}

// ListMeetingsByUser возвращает все встречи пользователя, новые — первыми.
func (r *Repository) ListMeetingsByUser(ctx context.Context, userID uuid.UUID) ([]models.Meeting, error) {
	const q = `
		SELECT id, user_id, title, file_path, file_size, mime_type, created_at, updated_at
		FROM meetings
		WHERE user_id = $1
		ORDER BY created_at DESC`

	rows, err := r.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list meetings: %w", err)
	}
	defer rows.Close()

	var meetings []models.Meeting
	for rows.Next() {
		m, err := scanMeeting(rows)
		if err != nil {
			return nil, err
		}
		meetings = append(meetings, m)
	}
	return meetings, rows.Err()
}

// ListMeetingsDetailed возвращает встречи пользователя с статусом и выжимкой.
func (r *Repository) ListMeetingsDetailed(ctx context.Context, userID uuid.UUID) ([]models.MeetingListItem, error) {
	const q = `
		SELECT m.id, m.title, m.created_at,
		       COALESCE(t.status, 'created'),
		       COALESCE(s.text, '')
		FROM meetings m
		LEFT JOIN tasks t  ON t.meeting_id = m.id
		LEFT JOIN summaries s ON s.meeting_id = m.id
		WHERE m.user_id = $1
		ORDER BY m.created_at DESC`

	rows, err := r.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list meetings detailed: %w", err)
	}
	defer rows.Close()

	var items []models.MeetingListItem
	for rows.Next() {
		var item models.MeetingListItem
		if err := rows.Scan(&item.MeetingID, &item.Title, &item.CreatedAt, &item.Status, &item.Summary); err != nil {
			return nil, fmt.Errorf("scan meeting item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// --- Задачи ---

// GetTaskByMeetingID возвращает задачу обработки для встречи.
func (r *Repository) GetTaskByMeetingID(ctx context.Context, meetingID uuid.UUID) (models.Task, error) {
	const q = `
		SELECT id, meeting_id, status, error_message, retry_count, created_at, updated_at
		FROM tasks
		WHERE meeting_id = $1`

	return scanTask(r.db.QueryRowContext(ctx, q, meetingID))
}

// GetTasksByStatus возвращает задачи в указанных статусах (для восстановления воркера).
func (r *Repository) GetTasksByStatus(ctx context.Context, statuses ...models.TaskStatus) ([]models.Task, error) {
	if len(statuses) == 0 {
		return nil, nil
	}

	// Преобразуем статусы в []string.
	statusStrings := make([]string, len(statuses))
	for i, s := range statuses {
		statusStrings[i] = string(s)
	}

	const q = `
		SELECT id, meeting_id, status, error_message, retry_count, created_at, updated_at
		FROM tasks
		WHERE status = ANY($1::text[])
		ORDER BY created_at`

	rows, err := r.db.QueryContext(ctx, q, statusStrings)
	if err != nil {
		return nil, fmt.Errorf("get tasks by status: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// --- Транскрипции и выжимки ---

// GetTranscript возвращает транскрипцию встречи с проверкой прав владельца.
func (r *Repository) GetTranscript(ctx context.Context, userID, meetingID uuid.UUID) (models.Transcript, error) {
	const q = `
		SELECT tr.id, tr.meeting_id, tr.text, tr.created_at
		FROM transcripts tr
		JOIN meetings m ON m.id = tr.meeting_id
		WHERE tr.meeting_id = $1 AND m.user_id = $2`

	var t models.Transcript
	var id string
	err := r.db.QueryRowContext(ctx, q, meetingID, userID).
		Scan(&id, &t.MeetingID, &t.Text, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Transcript{}, models.ErrNotFound
	}
	if err != nil {
		return models.Transcript{}, fmt.Errorf("get transcript: %w", err)
	}
	t.ID, err = uuid.Parse(id)
	if err != nil {
		return models.Transcript{}, fmt.Errorf("parse transcript id: %w", err)
	}
	return t, nil
}

// GetSummary возвращает выжимку встречи с проверкой прав владельца.
func (r *Repository) GetSummary(ctx context.Context, userID, meetingID uuid.UUID) (models.Summary, error) {
	const q = `
		SELECT s.id, s.meeting_id, s.text, s.created_at
		FROM summaries s
		JOIN meetings m ON m.id = s.meeting_id
		WHERE s.meeting_id = $1 AND m.user_id = $2`

	var s models.Summary
	var id string
	err := r.db.QueryRowContext(ctx, q, meetingID, userID).
		Scan(&id, &s.MeetingID, &s.Text, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Summary{}, models.ErrNotFound
	}
	if err != nil {
		return models.Summary{}, fmt.Errorf("get summary: %w", err)
	}
	s.ID, err = uuid.Parse(id)
	if err != nil {
		return models.Summary{}, fmt.Errorf("parse summary id: %w", err)
	}
	return s, nil
}

// --- Поиск---

// SearchMeetings ищет по ключевым словам в транскрипциях и выжимках,
// доступных пользователю. Результат: id встречи, дата создания, статус, фрагмент текста.
func (r *Repository) SearchMeetings(ctx context.Context, userID uuid.UUID, query string) ([]models.SearchResult, error) {
	const q = `
		SELECT DISTINCT ON (m.id)
			m.id         AS meeting_id,
			m.created_at AS created_at,
			t.status     AS status,
			COALESCE(tr.text, s.text) AS snippet
		FROM meetings m
		JOIN tasks t ON t.meeting_id = m.id
		LEFT JOIN transcripts tr ON tr.meeting_id = m.id
		LEFT JOIN summaries s ON s.meeting_id = m.id
		WHERE m.user_id = $1
		  AND (tr.text ILIKE '%' || $2 || '%' OR s.text ILIKE '%' || $2 || '%')
		ORDER BY m.id, m.created_at DESC`

	rows, err := r.db.QueryContext(ctx, q, userID, query)
	if err != nil {
		return nil, fmt.Errorf("search meetings: %w", err)
	}
	defer rows.Close()

	var results []models.SearchResult
	for rows.Next() {
		var res models.SearchResult
		var id string
		if err := rows.Scan(&id, &res.CreatedAt, &res.Status, &res.Snippet); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		res.MeetingID, err = uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parse meeting id: %w", err)
		}
		results = append(results, res)
	}
	return results, rows.Err()
}

// --- Чат ---

// SaveChatMessage сохраняет сообщение в историю чата по встрече.
func (r *Repository) SaveChatMessage(ctx context.Context, msg models.ChatMessage) error {
	const q = `
		INSERT INTO chat_messages (meeting_id, user_id, role, message)
		VALUES ($1, $2, $3, $4)`

	_, err := r.db.ExecContext(ctx, q, msg.MeetingID, msg.UserID, string(msg.Role), msg.Message)
	if err != nil {
		return fmt.Errorf("save chat message: %w", err)
	}
	return nil
}

// ListChatHistory возвращает историю чата по встрече, доступной пользователю.
func (r *Repository) ListChatHistory(ctx context.Context, userID, meetingID uuid.UUID) ([]models.ChatMessage, error) {
	const q = `
		SELECT cm.id, cm.meeting_id, cm.user_id, cm.role, cm.message, cm.created_at
		FROM chat_messages cm
		JOIN meetings m ON m.id = cm.meeting_id
		WHERE cm.meeting_id = $1 AND m.user_id = $2
		ORDER BY cm.created_at`

	rows, err := r.db.QueryContext(ctx, q, meetingID, userID)
	if err != nil {
		return nil, fmt.Errorf("list chat history: %w", err)
	}
	defer rows.Close()

	var messages []models.ChatMessage
	for rows.Next() {
		var msg models.ChatMessage
		var id, meetingIDStr, userIDStr string
		if err := rows.Scan(&id, &meetingIDStr, &userIDStr, &msg.Role, &msg.Message, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chat message: %w", err)
		}
		msg.ID, err = uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parse message id: %w", err)
		}
		msg.MeetingID, err = uuid.Parse(meetingIDStr)
		if err != nil {
			return nil, fmt.Errorf("parse meeting id: %w", err)
		}
		msg.UserID, err = uuid.Parse(userIDStr)
		if err != nil {
			return nil, fmt.Errorf("parse user id: %w", err)
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// --- Сканеры строк ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (models.User, error) {
	var u models.User
	var id string
	err := row.Scan(&id, &u.ExternalID, &u.Username, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.User{}, models.ErrNotFound
	}
	if err != nil {
		return models.User{}, fmt.Errorf("scan user: %w", err)
	}
	u.ID, err = uuid.Parse(id)
	if err != nil {
		return models.User{}, fmt.Errorf("parse user id: %w", err)
	}
	return u, nil
}

func scanMeeting(row rowScanner) (models.Meeting, error) {
	var m models.Meeting
	var id, userID string
	err := row.Scan(&id, &userID, &m.Title, &m.FilePath, &m.FileSize, &m.MimeType, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Meeting{}, models.ErrNotFound
	}
	if err != nil {
		return models.Meeting{}, fmt.Errorf("scan meeting: %w", err)
	}
	m.ID, err = uuid.Parse(id)
	if err != nil {
		return models.Meeting{}, fmt.Errorf("parse meeting id: %w", err)
	}
	m.UserID, err = uuid.Parse(userID)
	if err != nil {
		return models.Meeting{}, fmt.Errorf("parse user id: %w", err)
	}
	return m, nil
}

func scanTask(row rowScanner) (models.Task, error) {
	var t models.Task
	var id, meetingID, status string
	err := row.Scan(&id, &meetingID, &status, &t.ErrorMessage, &t.RetryCount, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Task{}, models.ErrNotFound
	}
	if err != nil {
		return models.Task{}, fmt.Errorf("scan task: %w", err)
	}
	t.ID, err = uuid.Parse(id)
	if err != nil {
		return models.Task{}, fmt.Errorf("parse task id: %w", err)
	}
	t.MeetingID, err = uuid.Parse(meetingID)
	if err != nil {
		return models.Task{}, fmt.Errorf("parse meeting id: %w", err)
	}
	t.Status = models.TaskStatus(status)
	return t, nil
}
