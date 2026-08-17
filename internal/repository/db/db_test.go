package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqlOpen открывает соединение с БД через database/sql без применения миграций.
func sqlOpen(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// setupTest подключается к БД (из .env) и очищает таблицы между тестами.
// Миграции применяются приложением (go run ./cmd/bot) — тест открывает
// соединение напрямую через database/sql.
func setupTest(t *testing.T) *Repository {
	t.Helper()

	_ = godotenv.Load("../../.env")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Открываем соединение напрямую: goose-миграции применяет приложение,
	// путь migrations/ относителен корня и недоступен из тестового бинарника.
	sqlDB, err := sqlOpen(databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { sqlDB.Close() })

	// Очищаем данные между тестами.
	_, err = sqlDB.ExecContext(ctx, `
		TRUNCATE chat_messages, summaries, transcripts, task_events, tasks, meeting_inputs, meetings, users RESTART IDENTITY CASCADE`)
	require.NoError(t, err)

	return New(sqlDB)
}

// createTestUser создаёт пользователя с уникальным externalID.
func createTestUser(t *testing.T, r *Repository, seed int64) models.User {
	t.Helper()
	u, err := r.CreateUser(context.Background(), seed, "test_user")
	require.NoError(t, err)
	return u
}

// TestCreateMeetingWithTask_Rollback проверяет атомарность транзакции:
// создание встречи с несуществующим userID должно откатить ВСЮ транзакцию —
// ни встречи, ни задачи, ни события в БД не остаётся.
func TestCreateMeetingWithTask_Rollback(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	// Несуществующий пользователь — FK constraint нарушится на INSERT meetings.
	_, err := r.CreateMeetingWithTask(ctx, uuid.New(), "test", "telegram:1", 100, "audio/ogg", "", []byte("audio"))
	require.Error(t, err)

	// Ничего не должно остаться: встреч, задач, событий — 0.
	var meetings, tasks, events int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM meetings`).Scan(&meetings))
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM tasks`).Scan(&tasks))
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM task_events`).Scan(&events))

	assert.Equal(t, 0, meetings, "meetings must be rolled back")
	assert.Equal(t, 0, tasks, "tasks must be rolled back")
	assert.Equal(t, 0, events, "task_events must be rolled back")
}

// TestCreateMeetingWithTask_Success проверяет happy path:
// встреча + задача + событие создаются, статус задачи = created.
func TestCreateMeetingWithTask_Success(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	user := createTestUser(t, r, 1001)
	meeting, err := r.CreateMeetingWithTask(ctx, user.ID, "Планёрка", "telegram:42", 2048, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	assert.NotEqual(t, uuid.Nil, meeting.ID)
	assert.Equal(t, user.ID, meeting.UserID)
	assert.Equal(t, "Планёрка", meeting.Title)

	task, err := r.GetTaskByMeetingID(ctx, meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCreated, task.Status)

	var events int
	require.NoError(t, r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_events WHERE task_id = $1`, task.ID).Scan(&events))
	assert.Equal(t, 1, events, "must have one initial task_event")
}

// TestUpdateTaskStatus_AddsEvent проверяет, что каждый переход статуса
// дополняет историю task_events.
func TestUpdateTaskStatus_AddsEvent(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	user := createTestUser(t, r, 1002)
	meeting, err := r.CreateMeetingWithTask(ctx, user.ID, "test", "telegram:1", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	task, err := r.GetTaskByMeetingID(ctx, meeting.ID)
	require.NoError(t, err)

	// Переводим в processing → транскрибирована → завершена.
	require.NoError(t, r.UpdateTaskStatus(ctx, task.ID, models.StatusProcessing, "started"))
	require.NoError(t, r.SaveTranscript(ctx, meeting.ID, "текст стенограммы"))
	require.NoError(t, r.SaveSummary(ctx, meeting.ID, "краткая выжимка"))

	final, err := r.GetTaskByMeetingID(ctx, meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, final.Status)

	var events int
	require.NoError(t, r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_events WHERE task_id = $1`, task.ID).Scan(&events))
	assert.Equal(t, 4, events, "created + processing + transcribed + completed")
}

// TestSearchMeetings_UserIsolation проверяет права доступа: пользователь A
// не должен видеть встречи пользователя B, даже если текст совпадает.
func TestSearchMeetings_UserIsolation(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	userA := createTestUser(t, r, 2001)
	userB := createTestUser(t, r, 2002)

	meetingA, err := r.CreateMeetingWithTask(ctx, userA.ID, "A", "telegram:1", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)
	_, err = r.CreateMeetingWithTask(ctx, userB.ID, "B", "telegram:2", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	// Оба пользователя сохраняют одинаковый текст "секретный пароль".
	require.NoError(t, r.SaveTranscript(ctx, meetingA.ID, "секретный пароль от сервера"))
	// У B тоже есть встреча с этим словом, но доступ к ней есть только у B.
	meetingB, err := r.ListMeetingsByUser(ctx, userB.ID)
	require.NoError(t, err)
	require.Len(t, meetingB, 1)
	require.NoError(t, r.SaveTranscript(ctx, meetingB[0].ID, "секретный пароль от сервера"))

	// Поиск от имени A должен вернуть только встречу A.
	results, err := r.SearchMeetings(ctx, userA.ID, "секретный")
	require.NoError(t, err)
	require.Len(t, results, 1, "user A must see only own meeting")
	assert.Equal(t, meetingA.ID, results[0].MeetingID)
	assert.Contains(t, results[0].Snippet, "секретный")

	// Поля результата соответствуют ТЗ п.12.
	assert.NotEqual(t, time.Time{}, results[0].CreatedAt, "date created")
	assert.NotEmpty(t, results[0].Status, "processing status")
}

// TestGetMeetingByID_Forbidden проверяет, что чужая встреча возвращает ErrNotFound.
func TestGetMeetingByID_Forbidden(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	userA := createTestUser(t, r, 3001)
	userB := createTestUser(t, r, 3002)

	meeting, err := r.CreateMeetingWithTask(ctx, userA.ID, "A", "telegram:1", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	_, err = r.GetMeetingByID(ctx, userB.ID, meeting.ID)
	assert.ErrorIs(t, err, models.ErrNotFound, "user B must not see user A's meeting")
}

// TestSaveError проверяет фиксацию ошибки: статус failed + error_message.
func TestSaveError(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	user := createTestUser(t, r, 4001)
	meeting, err := r.CreateMeetingWithTask(ctx, user.ID, "test", "telegram:1", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	task, err := r.GetTaskByMeetingID(ctx, meeting.ID)
	require.NoError(t, err)

	err = r.SaveError(ctx, task.ID, "speech provider timeout")
	require.NoError(t, err)

	failed, err := r.GetTaskByMeetingID(ctx, meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusFailed, failed.Status)
	assert.Equal(t, "speech provider timeout", failed.ErrorMessage)
}

// TestGetTasksByStatus проверяет выборку задач по статусам для восстановления воркера.
func TestGetTasksByStatus(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	user := createTestUser(t, r, 5001)

	// Две встречи: одна остаётся created, вторая переводится в processing.
	m1, err := r.CreateMeetingWithTask(ctx, user.ID, "1", "telegram:1", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)
	m2, err := r.CreateMeetingWithTask(ctx, user.ID, "2", "telegram:2", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	t1, err := r.GetTaskByMeetingID(ctx, m1.ID)
	require.NoError(t, err)
	t2, err := r.GetTaskByMeetingID(ctx, m2.ID)
	require.NoError(t, err)
	require.NoError(t, r.UpdateTaskStatus(ctx, t2.ID, models.StatusProcessing, "started"))

	tasks, err := r.GetTasksByStatus(ctx, models.StatusCreated, models.StatusProcessing)
	require.NoError(t, err)
	assert.Len(t, tasks, 2)

	ids := map[uuid.UUID]bool{t1.ID: true, t2.ID: true}
	for _, task := range tasks {
		assert.True(t, ids[task.ID], "unexpected task: %s", task.ID)
	}
}

// TestSaveTranscript проверяет сохранение транскрипции и перевод статуса в transcribed.
func TestSaveTranscript(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	user := createTestUser(t, r, 6001)
	meeting, err := r.CreateMeetingWithTask(ctx, user.ID, "test", "telegram:1", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	err = r.SaveTranscript(ctx, meeting.ID, "текст транскрипции")
	require.NoError(t, err)

	// Транскрипция сохранена.
	tr, err := r.GetTranscript(ctx, user.ID, meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, "текст транскрипции", tr.Text)

	// Статус задачи переведён в transcribed.
	task, err := r.GetTaskByMeetingID(ctx, meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusTranscribed, task.Status)

	// В истории: created + transcribed.
	var events int
	require.NoError(t, r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_events WHERE task_id = $1`, task.ID).Scan(&events))
	assert.Equal(t, 2, events)
}

// TestSaveSummary проверяет сохранение выжимки и перевод статуса в completed.
func TestSaveSummary(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	user := createTestUser(t, r, 6002)
	meeting, err := r.CreateMeetingWithTask(ctx, user.ID, "test", "telegram:1", 10, "audio/ogg", "", []byte("audio"))
	require.NoError(t, err)

	err = r.SaveSummary(ctx, meeting.ID, "краткая выжимка")
	require.NoError(t, err)

	// Выжимка сохранена.
	s, err := r.GetSummary(ctx, user.ID, meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, "краткая выжимка", s.Text)

	// Статус задачи переведён в completed.
	task, err := r.GetTaskByMeetingID(ctx, meeting.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, task.Status)

	// В истории: created + completed.
	var events int
	require.NoError(t, r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_events WHERE task_id = $1`, task.ID).Scan(&events))
	assert.Equal(t, 2, events)
}

// TestSaveTranscript_Rollback проверяет атомарность SaveTranscript:
// если задача не найдена (встречи нет), транскрипция НЕ должна сохраниться.
func TestSaveTranscript_Rollback(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	// Несуществующая встреча → INSERT transcript упадёт на FK, всё откатится.
	err := r.SaveTranscript(ctx, uuid.New(), "текст")
	require.Error(t, err)

	var transcripts int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM transcripts`).Scan(&transcripts))
	assert.Equal(t, 0, transcripts, "transcript must be rolled back")

	// И задача не должна перейти в transcribed.
	var tasks int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM tasks`).Scan(&tasks))
	assert.Equal(t, 0, tasks, "no tasks must exist")
}

// TestUpdateTaskStatus_NotFound проверяет, что обновление статуса
// несуществующей задачи возвращает ErrNotFound и не создаёт событий.
func TestUpdateTaskStatus_NotFound(t *testing.T) {
	r := setupTest(t)
	ctx := context.Background()

	err := r.UpdateTaskStatus(ctx, uuid.New(), models.StatusProcessing, "started")
	assert.ErrorIs(t, err, models.ErrNotFound, "unknown task must return ErrNotFound")

	var events int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM task_events`).Scan(&events))
	assert.Equal(t, 0, events, "no events must be created for unknown task")
}
