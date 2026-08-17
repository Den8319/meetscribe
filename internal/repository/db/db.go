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

	return scanUser(r.db.QueryRowContext(ctx, q, externalID, username))
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

// GetMeetingInput возвращает входные данные встречи (текст/аудио) из БД.
func (r *Repository) GetMeetingInput(ctx context.Context, meetingID uuid.UUID) (models.MeetingInput, error) {
	const q = `
		SELECT mi.meeting_id, mi.input_type, mi.text, mi.audio_data, m.mime_type
		FROM meeting_inputs mi
		JOIN meetings m ON m.id = mi.meeting_id
		WHERE mi.meeting_id = $1`

	return scanMeetingInput(r.db.QueryRowContext(ctx, q, meetingID))
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

	return scanRows(rows, scanMeeting)
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

	return scanRows(rows, scanMeetingListItem)
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

	return scanRows(rows, scanTask)
}

// --- Транскрипции и выжимки ---

// GetTranscript возвращает транскрипцию встречи с проверкой прав владельца.
func (r *Repository) GetTranscript(ctx context.Context, userID, meetingID uuid.UUID) (models.Transcript, error) {
	const q = `
		SELECT tr.id, tr.meeting_id, tr.text, tr.created_at
		FROM transcripts tr
		JOIN meetings m ON m.id = tr.meeting_id
		WHERE tr.meeting_id = $1 AND m.user_id = $2`

	return scanTranscript(r.db.QueryRowContext(ctx, q, meetingID, userID))
}

// GetSummary возвращает выжимку встречи с проверкой прав владельца.
func (r *Repository) GetSummary(ctx context.Context, userID, meetingID uuid.UUID) (models.Summary, error) {
	const q = `
		SELECT s.id, s.meeting_id, s.text, s.created_at
		FROM summaries s
		JOIN meetings m ON m.id = s.meeting_id
		WHERE s.meeting_id = $1 AND m.user_id = $2`

	return scanSummary(r.db.QueryRowContext(ctx, q, meetingID, userID))
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

	return scanRows(rows, scanSearchResult)
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

	return scanRows(rows, scanChatMessage)
}

// --- Generic-хелперы для сканирования строк ---

type rowScanner interface {
	Scan(dest ...any) error
}

// uuidBinder связывает строковую переменную из sql.Scan с целевым полем uuid.UUID.
// Register создаёт промежуточную *string для Scan и запоминает связь.
// parseAndBind парсит все зарегистрированные строки и раскладывает по местам.
type uuidBinder struct {
	bindings []uuidBinding
}

type uuidBinding struct {
	src  *string
	dest *uuid.UUID
}

// Register создаёт связь между колонкой БД (строка) и полем структуры (uuid.UUID).
// Возвращает *string для передачи в row.Scan.
func (b *uuidBinder) Register(dest *uuid.UUID) *string {
	var v string
	b.bindings = append(b.bindings, uuidBinding{src: &v, dest: dest})
	return &v
}

// parseAndBind парсит все зарегистрированные строки в uuid.UUID.
func (b *uuidBinder) parseAndBind(entityName string) error {
	for _, bind := range b.bindings {
		id, err := uuid.Parse(*bind.src)
		if err != nil {
			return fmt.Errorf("parse %s id: %w", entityName, err)
		}
		*bind.dest = id
	}
	return nil
}

// scanRow — универсальный хелпер: сканирует строку, парсит UUID, оборачивает ошибки.
// scanFn вызывает scanner.Scan самостоятельно и может делать пост-обработку
// (например, конверсию string → TaskStatus).
func scanRow[T any](
	scanner rowScanner,
	entityName string,
	scanFn func(dest *T, b *uuidBinder) error,
) (T, error) {
	var entity T
	b := &uuidBinder{}

	err := scanFn(&entity, b)
	if errors.Is(err, sql.ErrNoRows) {
		return entity, models.ErrNotFound
	}
	if err != nil {
		return entity, fmt.Errorf("scan %s: %w", entityName, err)
	}

	if err := b.parseAndBind(entityName); err != nil {
		return entity, err
	}
	return entity, nil
}

// scanRows — универсальный хелпер для цикла по sql.Rows.
// Гарантированно закрывает rows при любом исходе (включая ошибку).
func scanRows[T any](rows *sql.Rows, scanFn func(rowScanner) (T, error)) ([]T, error) {
	defer rows.Close()

	var items []T
	for rows.Next() {
		item, err := scanFn(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return items, nil
}

// --- Сканеры сущностей ---

func scanUser(row rowScanner) (models.User, error) {
	return scanRow(row, "user", func(u *models.User, b *uuidBinder) error {
		return row.Scan(b.Register(&u.ID), &u.ExternalID, &u.Username, &u.CreatedAt)
	})
}

func scanMeeting(row rowScanner) (models.Meeting, error) {
	return scanRow(row, "meeting", func(m *models.Meeting, b *uuidBinder) error {
		return row.Scan(
			b.Register(&m.ID), b.Register(&m.UserID),
			&m.Title, &m.FilePath, &m.FileSize, &m.MimeType,
			&m.CreatedAt, &m.UpdatedAt,
		)
	})
}

func scanTask(row rowScanner) (models.Task, error) {
	return scanRow(row, "task", func(t *models.Task, b *uuidBinder) error {
		var status string
		err := row.Scan(
			b.Register(&t.ID), b.Register(&t.MeetingID),
			&status, &t.ErrorMessage, &t.RetryCount,
			&t.CreatedAt, &t.UpdatedAt,
		)
		if err != nil {
			return err
		}
		t.Status = models.TaskStatus(status)
		return nil
	})
}

func scanTranscript(row rowScanner) (models.Transcript, error) {
	return scanRow(row, "transcript", func(t *models.Transcript, b *uuidBinder) error {
		return row.Scan(b.Register(&t.ID), b.Register(&t.MeetingID), &t.Text, &t.CreatedAt)
	})
}

func scanSummary(row rowScanner) (models.Summary, error) {
	return scanRow(row, "summary", func(s *models.Summary, b *uuidBinder) error {
		return row.Scan(b.Register(&s.ID), b.Register(&s.MeetingID), &s.Text, &s.CreatedAt)
	})
}

func scanSearchResult(row rowScanner) (models.SearchResult, error) {
	return scanRow(row, "search result", func(r *models.SearchResult, b *uuidBinder) error {
		var status string
		err := row.Scan(b.Register(&r.MeetingID), &r.CreatedAt, &status, &r.Snippet)
		if err != nil {
			return err
		}
		r.Status = models.TaskStatus(status)
		return nil
	})
}

func scanChatMessage(row rowScanner) (models.ChatMessage, error) {
	return scanRow(row, "chat message", func(m *models.ChatMessage, b *uuidBinder) error {
		var role string
		err := row.Scan(
			b.Register(&m.ID), b.Register(&m.MeetingID), b.Register(&m.UserID),
			&role, &m.Message, &m.CreatedAt,
		)
		if err != nil {
			return err
		}
		m.Role = models.ChatRole(role)
		return nil
	})
}

func scanMeetingListItem(row rowScanner) (models.MeetingListItem, error) {
	return scanRow(row, "meeting item", func(item *models.MeetingListItem, b *uuidBinder) error {
		var status string
		err := row.Scan(b.Register(&item.MeetingID), &item.Title, &item.CreatedAt, &status, &item.Summary)
		if err != nil {
			return err
		}
		item.Status = models.TaskStatus(status)
		return nil
	})
}

func scanMeetingInput(row rowScanner) (models.MeetingInput, error) {
	return scanRow(row, "meeting input", func(in *models.MeetingInput, b *uuidBinder) error {
		return row.Scan(b.Register(&in.MeetingID), &in.Type, &in.Text, &in.Audio, &in.MimeType)
	})
}
