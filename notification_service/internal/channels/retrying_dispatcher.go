package channels

import (
	"context"
	"log/slog"
	"math/rand"
	"time"
)

// RetryingDispatcher wraps another dispatcher and retries transient send failures.
type RetryingDispatcher struct {
	inner      Dispatcher
	maxRetries int
	baseDelay  time.Duration
}

// NewRetryingDispatcher decorates a dispatcher with exponential-backoff retries.
func NewRetryingDispatcher(inner Dispatcher, maxRetries int, baseDelay time.Duration) Dispatcher {
	if maxRetries < 0 {
		maxRetries = 0
	}
	if baseDelay < 0 {
		baseDelay = 0
	}
	return &RetryingDispatcher{
		inner:      inner,
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
	}
}

func (r *RetryingDispatcher) Name() string {
	return r.inner.Name()
}

func (r *RetryingDispatcher) Send(ctx context.Context, recipient, message string) error {
	var lastErr error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		if attempt > 0 {
			delay := withJitter(r.baseDelay * (1 << (attempt - 1)))
			slog.Warn("retrying notification dispatch",
				"channel", r.inner.Name(),
				"attempt", attempt,
				"delay", delay,
			)
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}

		if err := r.inner.Send(ctx, recipient, message); err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	return lastErr
}

func withJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	spread := delay / 4
	if spread <= 0 {
		return delay
	}
	return delay - spread + time.Duration(rand.Int63n(int64(2*spread)+1))
}
