// Package opt содержит общие функциональные опции для mock-клиентов.
package opt

import "time"

type Option[T any] func(*T)

// Delayer — контракт объекта с настраиваемой задержкой.
type Delayer interface {
	SetDelay(min, max time.Duration)
}

// WithDelay задаёт диапазон задержки mock-клиента.
// Generic: T — тип клиента, *T должен реализовывать Delayer.
// PT — псевдоним указателя (выводится автоматически как *T).
func WithDelay[T any, PT interface {
	*T
	Delayer
}](min, max time.Duration) Option[T] {
	return func(t *T) {
		PT(t).SetDelay(min, max)
	}
}
