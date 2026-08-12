package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// Job — единица работы для воркера.
type Job struct {
	MeetingID uuid.UUID
	Audio     []byte
	MimeType  string
	Text      string
}

// SpeechClient — контракт распознавания речи, необходимый воркеру.
type SpeechClient interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error)
}

// WorkerPool управляет фоновой обработкой аудио.
type WorkerPool struct {
	storage       Storage
	speech        SpeechClient
	llm           LLMClient
	concurrency   int
	speechTimeout time.Duration
	llmTimeout    time.Duration

	queue chan Job
	g     *errgroup.Group
	ctx   context.Context // контекст воркеров (дочерний от shutdown)

	mu     sync.Mutex
	active map[uuid.UUID]bool // встречи в обработке (для дедупликации)
}

// NewWorkerPool создаёт пул воркеров.
func NewWorkerPool(
	storage Storage,
	speech SpeechClient,
	llm LLMClient,
	concurrency int,
	speechTimeout, llmTimeout time.Duration,
	queueSize int,
) *WorkerPool {
	if queueSize < concurrency {
		queueSize = concurrency * 2
	}
	return &WorkerPool{
		storage:       storage,
		speech:        speech,
		llm:           llm,
		concurrency:   concurrency,
		speechTimeout: speechTimeout,
		llmTimeout:    llmTimeout,
		queue:         make(chan Job, queueSize),
		active:        make(map[uuid.UUID]bool),
	}
}

// Start запускает N воркеров через errgroup и фоновый sweeper.
func (w *WorkerPool) Start(ctx context.Context) {
	w.ctx = ctx
	w.g, ctx = errgroup.WithContext(ctx)

	for i := 0; i < w.concurrency; i++ {
		w.g.Go(func() error {
			return w.worker(ctx, i)
		})
	}

	// Фоновый sweeper: раз в 10 секунд ищет зависшие задачи (created/processing)
	// и отправляет их в очередь. Audio=nil → воркер пометит failed.
	go w.sweeper(ctx)
}

// Submit отправляет задачу в очередь. Неблокирующая: если очередь полна,
// задача останется в БД со статусом created и будет подобрана sweeper'ом.
func (w *WorkerPool) Submit(job Job) bool {
	select {
	case w.queue <- job:
		return true
	default:
		slog.Warn("worker queue full, task will be picked up by sweeper",
			"meeting_id", job.MeetingID)
		return false
	}
}

// Recover загружает незавершённые задачи из БД после рестарта.
func (w *WorkerPool) Recover(ctx context.Context) error {
	tasks, err := w.storage.GetTasksByStatus(ctx, models.StatusCreated, models.StatusProcessing)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		w.Submit(Job{MeetingID: task.MeetingID, Audio: nil})
	}
	slog.Info("recovery completed", "tasks", len(tasks))
	return nil
}

// Wait дожидается завершения всех воркеров (используется при shutdown).
func (w *WorkerPool) Wait() error {
	return w.g.Wait()
}

// Stop инициирует graceful shutdown: закрывает очередь, ждёт воркеры.
func (w *WorkerPool) Stop() {
	close(w.queue)
}

// worker — цикл обработки одного воркера.
func (w *WorkerPool) worker(ctx context.Context, id int) error {
	for {
		select {
		case <-ctx.Done():
			slog.Info("worker stopped", "worker_id", id)
			return nil
		case job, ok := <-w.queue:
			if !ok {
				// Канал закрыт (shutdown).
				slog.Info("worker stopped (queue closed)", "worker_id", id)
				return nil
			}
			w.processJob(ctx, job)
		}
	}
}

// processJob обрабатывает одну встречу: transcribe → save → summarize → save.
func (w *WorkerPool) processJob(ctx context.Context, job Job) {
	w.markActive(job.MeetingID, true)
	defer w.markActive(job.MeetingID, false)

	task, err := w.storage.GetTaskByMeetingID(ctx, job.MeetingID)
	if err != nil {
		slog.Error("worker: get task failed", "meeting_id", job.MeetingID, "error", err)
		return
	}

	start := time.Now()
	slog.Info("worker: processing started",
		"meeting_id", job.MeetingID, "task_id", task.ID, "worker", "pool")

	// 1. processing
	if err := w.storage.UpdateTaskStatus(ctx, task.ID, models.StatusProcessing, "worker picked up"); err != nil {
		slog.Error("worker: set processing failed", "task_id", task.ID, "error", err)
		return
	}

	// 2. Получить транскрипцию
	var transcript string

	switch {
	case job.Text != "":
		// Текстовый ввод — речь не распознаём, текст и есть транскрипция.
		transcript = job.Text
		if err := w.storage.SaveTranscript(ctx, job.MeetingID, transcript); err != nil {
			w.failTask(ctx, task.ID, "save transcript: "+err.Error())
			return
		}
		slog.Info("worker: text meeting saved as transcript",
			"meeting_id", job.MeetingID, "task_id", task.ID)

	case job.Audio == nil:
		// Аудио без данных (после рестарта байты утеряны).
		w.failTask(ctx, task.ID, "audio data lost after restart")
		return

	default:
		// Аудио: Transcribe с таймаутом speech.
		speechCtx, speechCancel := context.WithTimeout(ctx, w.speechTimeout)
		defer speechCancel()

		slog.Info("speech transcribe requested",
			"meeting_id", job.MeetingID, "task_id", task.ID,
			"audio_size", len(job.Audio), "mime_type", job.MimeType)
		transcript, err = w.speech.Transcribe(speechCtx, job.Audio, job.MimeType)
		if err != nil {
			slog.Error("speech transcribe failed", "meeting_id", job.MeetingID, "task_id", task.ID, "error", err)
			if errors.Is(err, context.DeadlineExceeded) {
				w.failTask(ctx, task.ID, "speech recognition timeout")
			} else {
				w.failTask(ctx, task.ID, "speech recognition error: "+err.Error())
			}
			return
		}

		// Сохранить транскрипцию → transcribed.
		if err := w.storage.SaveTranscript(ctx, job.MeetingID, transcript); err != nil {
			w.failTask(ctx, task.ID, "save transcript: "+err.Error())
			return
		}
		slog.Info("transcript saved",
			"meeting_id", job.MeetingID, "task_id", task.ID,
			"transcript_len", len(transcript))
	}

	// 3. Summarize
	llmCtx, llmCancel := context.WithTimeout(ctx, w.llmTimeout)
	defer llmCancel()

	slog.Info("llm summarize requested",
		"meeting_id", job.MeetingID, "task_id", task.ID,
		"transcript_len", len(transcript))
	summary, err := w.llm.Summarize(llmCtx, transcript)
	if err != nil {
		slog.Error("llm summarize failed", "meeting_id", job.MeetingID, "task_id", task.ID, "error", err)
		if errors.Is(err, context.DeadlineExceeded) {
			w.failTask(ctx, task.ID, "llm summary timeout")
		} else {
			w.failTask(ctx, task.ID, "llm summary error: "+err.Error())
		}
		return
	}

	// 4. Сохранить выжимку → completed.
	if err := w.storage.SaveSummary(ctx, job.MeetingID, summary); err != nil {
		w.failTask(ctx, task.ID, "save summary: "+err.Error())
		return
	}
	slog.Info("summary saved",
		"meeting_id", job.MeetingID, "task_id", task.ID,
		"summary_len", len(summary))

	slog.Info("worker: processing completed",
		"meeting_id", job.MeetingID,
		"task_id", task.ID,
		"duration_ms", time.Since(start).Milliseconds())
}

// failTask переводит задачу в failed и логирует ошибку.
func (w *WorkerPool) failTask(ctx context.Context, taskID uuid.UUID, msg string) {
	slog.Error("worker: task failed", "task_id", taskID, "error", msg)
	if err := w.storage.SaveError(ctx, taskID, msg); err != nil {
		slog.Error("worker: save error failed", "task_id", taskID, "error", err)
	}
}

// sweeper периодически ищет зависшие задачи (created/processing)
// и отправляет их в очередь.
func (w *WorkerPool) sweeper(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}


func (w *WorkerPool) sweep(ctx context.Context) {
	tasks, err := w.storage.GetTasksByStatus(ctx, models.StatusCreated, models.StatusProcessing)
	if err != nil {
		slog.Error("sweeper: get tasks failed", "error", err)
		return
	}

	for _, task := range tasks {
		// Пропускаем задачи, которые уже обрабатываются воркером.
		if w.isActive(task.MeetingID) {
			continue
		}
		w.Submit(Job{MeetingID: task.MeetingID, Audio: nil})
	}
}

// markActive / isActive — трекинг встреч в обработке (дедупликация sweeper'а).
func (w *WorkerPool) markActive(meetingID uuid.UUID, active bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if active {
		w.active[meetingID] = true
	} else {
		delete(w.active, meetingID)
	}
}

func (w *WorkerPool) isActive(meetingID uuid.UUID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active[meetingID]
}
