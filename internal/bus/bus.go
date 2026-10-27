// Package bus is the in-process event bus that connects the agent's subsystems.
package bus

import (
	"context"
	"sync"
)

// Bus fans every published value out to all subscribers.
type Bus[T any] struct {
	mu   sync.RWMutex
	subs []chan T
	buf  int
}

// New returns a bus whose subscriber channels hold up to buf pending values.
func New[T any](buf int) *Bus[T] {
	return &Bus[T]{buf: buf}
}

// Subscribe returns a channel that receives every value published after the call.
func (b *Bus[T]) Subscribe() <-chan T {
	ch := make(chan T, b.buf)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

// Publish delivers v to every subscriber, blocking while a subscriber's buffer is full.
func (b *Bus[T]) Publish(ctx context.Context, v T) error {
	b.mu.RLock()
	subs := b.subs
	b.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- v:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
