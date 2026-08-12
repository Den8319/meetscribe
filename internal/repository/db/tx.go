package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
)

// withTx выполняет fn внутри транзакции: commit при успехе, rollback при ошибке.
func withTx(ctx context.Context, sqlDB *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// updateTaskStatusTx обновляет статус задачи и добавляет запись в task_events.
// Единая точка перехода статусов: вызывается из UpdateTaskStatus, SaveTranscript,
// SaveSummary, SaveError — всегда в контексте уже открытой транзакции.
func updateTaskStatusTx(ctx context.Context, tx *sql.Tx, taskID uuid.UUID, status models.TaskStatus, msg string) error {
	const updateTask = `
		UPDATE tasks
		SET status = $1, error_message = $3, updated_at = now()
		WHERE id = $2`

	res, err := tx.ExecContext(ctx, updateTask, string(status), taskID, msg)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return models.ErrNotFound
	}

	const insertEvent = `
		INSERT INTO task_events (task_id, status, message)
		VALUES ($1, $2, $3)`

	if _, err := tx.ExecContext(ctx, insertEvent, taskID, string(status), msg); err != nil {
		return fmt.Errorf("insert task event: %w", err)
	}
	return nil
}

// --- Встречи ---

// CreateMeetingWithTask создаёт встречу, задачу (status=created) и первое событие
// в одной транзакции: если любой шаг падает, вся встреча откатывается.
func (r *Repository) CreateMeetingWithTask(ctx context.Context, userID uuid.UUID, title, filePath string, fileSize int64, mimeType string) (models.Meeting, error) {
	var meeting models.Meeting

	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		const insertMeeting = `
			INSERT INTO meetings (user_id, title, file_path, file_size, mime_type)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, user_id, title, file_path, file_size, mime_type, created_at, updated_at`

		var id, userIDStr string
		if err := tx.QueryRowContext(ctx, insertMeeting, userID, title, filePath, fileSize, mimeType).
			Scan(&id, &userIDStr, &meeting.Title, &meeting.FilePath, &meeting.FileSize, &meeting.MimeType, &meeting.CreatedAt, &meeting.UpdatedAt); err != nil {
			return fmt.Errorf("insert meeting: %w", err)
		}

		var err error
		meeting.ID, err = uuid.Parse(id)
		if err != nil {
			return fmt.Errorf("parse meeting id: %w", err)
		}
		meeting.UserID, err = uuid.Parse(userIDStr)
		if err != nil {
			return fmt.Errorf("parse user id: %w", err)
		}

		const insertTask = `
			INSERT INTO tasks (meeting_id, status)
			VALUES ($1, $2)
			RETURNING id`

		var taskID string
		if err := tx.QueryRowContext(ctx, insertTask, meeting.ID, string(models.StatusCreated)).
			Scan(&taskID); err != nil {
			return fmt.Errorf("insert task: %w", err)
		}
		parsedTaskID, err := uuid.Parse(taskID)
		if err != nil {
			return fmt.Errorf("parse task id: %w", err)
		}

		// Первое событие в истории задачи.
		if err := updateTaskStatusTx(ctx, tx, parsedTaskID, models.StatusCreated, "meeting created"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return models.Meeting{}, err
	}
	return meeting, nil
}

// --- Задачи ---

// UpdateTaskStatus переводит задачу в новый статус и пишет событие в task_events.
func (r *Repository) UpdateTaskStatus(ctx context.Context, taskID uuid.UUID, status models.TaskStatus, msg string) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		return updateTaskStatusTx(ctx, tx, taskID, status, msg)
	})
}

// SaveError фиксирует ошибку обработки: статус failed + текст ошибки.
func (r *Repository) SaveError(ctx context.Context, taskID uuid.UUID, errMsg string) error {
	return r.UpdateTaskStatus(ctx, taskID, models.StatusFailed, errMsg)
}

// --- Транскрипции и выжимки ---

// SaveTranscript сохраняет транскрипцию и переводит задачу в статус transcribed.
func (r *Repository) SaveTranscript(ctx context.Context, meetingID uuid.UUID, text string) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		const insertTranscript = `
			INSERT INTO transcripts (meeting_id, text)
			VALUES ($1, $2)`

		if _, err := tx.ExecContext(ctx, insertTranscript, meetingID, text); err != nil {
			return fmt.Errorf("insert transcript: %w", err)
		}

		taskID, err := taskIDByMeetingID(ctx, tx, meetingID)
		if err != nil {
			return err
		}
		return updateTaskStatusTx(ctx, tx, taskID, models.StatusTranscribed, "transcript saved")
	})
}

// SaveSummary сохраняет выжимку и переводит задачу в статус completed.
func (r *Repository) SaveSummary(ctx context.Context, meetingID uuid.UUID, text string) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		const insertSummary = `
			INSERT INTO summaries (meeting_id, text)
			VALUES ($1, $2)`

		if _, err := tx.ExecContext(ctx, insertSummary, meetingID, text); err != nil {
			return fmt.Errorf("insert summary: %w", err)
		}

		taskID, err := taskIDByMeetingID(ctx, tx, meetingID)
		if err != nil {
			return err
		}
		return updateTaskStatusTx(ctx, tx, taskID, models.StatusCompleted, "summary saved")
	})
}

// taskIDByMeetingID находит задачу встречи внутри транзакции.
func taskIDByMeetingID(ctx context.Context, tx *sql.Tx, meetingID uuid.UUID) (uuid.UUID, error) {
	const q = `
		SELECT id
		FROM tasks
		WHERE meeting_id = $1`

	var id string
	if err := tx.QueryRowContext(ctx, q, meetingID).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return uuid.Nil, models.ErrNotFound
		}
		return uuid.Nil, fmt.Errorf("get task by meeting id: %w", err)
	}
	return uuid.Parse(id)
}
