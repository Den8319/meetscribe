// Package speech предоставляет клиент для распознавания речи (speech-to-text).
package speech

import "context"

// Client — интерфейс распознавания речи.
type Client interface {
	// Transcribe распознаёт речь в аудио и транскрипцую.
	Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error)
}
