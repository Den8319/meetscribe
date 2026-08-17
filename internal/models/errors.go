// Package models содержит доменные ошибки приложения.
package models

import "errors"

var (
	
	ErrNotFound = errors.New("not found")

	ErrTaskNotFailed = errors.New("task is not in failed status")

	ErrInvalidInput = errors.New("invalid input")

	ErrFileTooLarge = errors.New("file too large")

	ErrUnsupportedFormat = errors.New("unsupported file format")

	ErrNotProcessed = errors.New("meeting is not processed yet")
)

// IsNotFound возвращает true, если ошибка является ErrNotFound
// (в том числе обёрнутой через fmt.Errorf с %w).
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
