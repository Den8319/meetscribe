// Package llm предоставляет клиент для работы с языковой моделью (LLM).
package llm

import (
	"context"

	"github.com/Den8319/meetscribe/internal/models"
)

// Client — интерфейс языковой модели.
type Client interface {
	// Summarize генерирует краткую выжимку встречи по тексту транскрипции.
	Summarize(ctx context.Context, transcript string) (string, error)

	// Chat отвечает на вопрос пользователя по контексту встречи.
	// contextText — транскрипция/выжимка встречи, history — предыдущие сообщения чата,
	// question — вопрос пользователя.
	Chat(ctx context.Context, contextText string, history []models.ChatMessage, question string) (string, error)
}
