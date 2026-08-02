package channels

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flakyDispatcher struct {
	name         string
	failuresLeft int
	attempts     int
}

func (f *flakyDispatcher) Name() string { return f.name }

func (f *flakyDispatcher) Send(ctx context.Context, recipient, message string) error {
	f.attempts++
	if f.failuresLeft > 0 {
		f.failuresLeft--
		return errors.New("transient failure")
	}
	return nil
}

func TestRetryingDispatcherEventuallySucceeds(t *testing.T) {
	inner := &flakyDispatcher{name: "email", failuresLeft: 2}
	dispatcher := NewRetryingDispatcher(inner, 3, time.Millisecond)

	if err := dispatcher.Send(context.Background(), "test@example.com", "hello"); err != nil {
		t.Fatalf("expected retrying dispatcher to succeed, got %v", err)
	}
	if inner.attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", inner.attempts)
	}
}

func TestRetryingDispatcherReturnsLastError(t *testing.T) {
	inner := &flakyDispatcher{name: "email", failuresLeft: 5}
	dispatcher := NewRetryingDispatcher(inner, 2, time.Millisecond)

	err := dispatcher.Send(context.Background(), "test@example.com", "hello")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if inner.attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", inner.attempts)
	}
}