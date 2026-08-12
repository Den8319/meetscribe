// Package main — точка входа Telegram-бота MeetScribe.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Den8319/meetscribe/internal/config"
	"github.com/Den8319/meetscribe/internal/repository/db"
	"github.com/Den8319/meetscribe/internal/repository/llm"
	"github.com/Den8319/meetscribe/internal/repository/speech"
	"github.com/Den8319/meetscribe/internal/service"
	"github.com/Den8319/meetscribe/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// Логгер
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Конфигурация
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.Info("config loaded", "speech_provider", cfg.SpeechProvider, "llm_provider", cfg.LLMProvider)

	// Корневой контекст
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	// База данных
	sqlDB, err := db.Connect(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	slog.Info("database connected, migrations applied")

	// Репозиторий (реализация Storage)
	storage := db.New(sqlDB)

	// Внешние клиенты
	llmClient := llm.NewClient(cfg)
	speechClient := speech.NewClient(cfg)

	// Бизнес-логика
	meetingService := service.NewMeetingService(storage, llmClient)

	// Воркер-пул
	wp := service.NewWorkerPool(
		storage,
		speechClient,
		llmClient,
		cfg.WorkerConcurrency,
		cfg.SpeechTimeout,
		cfg.LLMTimeout,
		cfg.WorkerConcurrency*2,
	)

	// Восстановление задач после рестарта
	if err := wp.Recover(rootCtx); err != nil {
		slog.Warn("recovery failed, continuing", "error", err)
	}

	// Запуск воркеров
	wp.Start(rootCtx)

	// Telegram-бот
	tgBot, err := telegram.New(cfg.TelegramBotToken, meetingService, wp)
	if err != nil {
		return err
	}

	// Запускаем бота в отдельной горутине
	go func() {
		tgBot.Start()
	}()

	slog.Info("application started", "worker_concurrency", cfg.WorkerConcurrency)

	// --- Graceful shutdown ---
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("shutdown signal received")

	// 1. Останавливаем бота (перестаёт принимать новые сообщения).
	tgBot.Stop()

	// 2. Отменяем корневой контекст → воркеры получают отмену,
	//    внешние запросы (speech/LLM) прерываются.
	cancelRoot()

	// 2. Закрываем очередь → воркеры дообрабатывают остаток и выходят.
	wp.Stop()

	// 3. Ждём воркеры с таймаутом (errgroup).
	done := make(chan error, 1)
	go func() {
		done <- wp.Wait()
	}()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("worker pool finished with error", "error", err)
		}
		slog.Info("all workers stopped")
	case <-shutdownCtx.Done():
		slog.Warn("shutdown timeout exceeded, forcing stop",
			"timeout", cfg.ShutdownTimeout)
	}

	slog.Info("application stopped gracefully")
	return nil
}
