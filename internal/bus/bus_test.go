package bus

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPublishReachesAllSubscribers(t *testing.T) {
	b := New[int](1)
	a, c := b.Subscribe(), b.Subscribe()

	if err := b.Publish(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan int{a, c} {
		if got := <-ch; got != 7 {
			t.Fatalf("got %d, want 7", got)
		}
	}
}

func TestPublishStopsOnContextWhenFull(t *testing.T) {
	b := New[int](0)
	b.Subscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := b.Publish(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
}
