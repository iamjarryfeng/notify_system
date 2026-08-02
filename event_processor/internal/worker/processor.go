package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/jarryfeng/notify_system/event_processor/internal/config"
	"github.com/jarryfeng/notify_system/event_processor/internal/models"
	"github.com/jarryfeng/notify_system/event_processor/internal/repository"
)

const queueKey = "events:queue"

// Processor picks events off the queue and dispatches them.
type Processor struct {
	repo              repository.EventRepository
	redisClient       *redis.Client
	notificationURL   string
	httpClient        *http.Client
	maxRetries        int
	retryBaseDelay    time.Duration
	done              chan struct{}
	reconcileDone     chan struct{}
	doneOnce          sync.Once
	reconcileDoneOnce sync.Once
}

// NewProcessor constructs a Processor.
func NewProcessor(
	repo repository.EventRepository,
	rdb *redis.Client,
	cfg *config.Config,
) *Processor {
	return &Processor{
		repo:            repo,
		redisClient:     rdb,
		notificationURL: cfg.NotificationServiceURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		maxRetries:     cfg.MaxRetries,
		retryBaseDelay: cfg.RetryBaseDelay,
		done:           make(chan struct{}),
		reconcileDone:  make(chan struct{}),
	}
}

// Run starts the processing loop. It should be run in a goroutine.
func (p *Processor) Run(ctx context.Context) {
	defer p.doneOnce.Do(func() {
		close(p.done)
	})

	slog.Info("event processor worker started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("event processor worker shutting down")
			return
		default:
		}

		pollCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		result, err := p.redisClient.BLPop(pollCtx, 1*time.Second, queueKey).Result()
		cancel()
		if err == redis.Nil || err == context.Canceled || err == context.DeadlineExceeded {
			continue
		}
		if err != nil {
			slog.Error("redis blpop error", "error", err)
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			continue
		}

		eventID := result[1]
		p.processOne(context.WithoutCancel(ctx), eventID)
	}
}

// RunReconciler periodically re-enqueues stale pending events. This mitigates
// the DB-write/Redis-enqueue split by giving persisted but unqueued events a
// recovery path.
func (p *Processor) RunReconciler(ctx context.Context, interval, staleAfter time.Duration, limit int) {
	defer p.reconcileDoneOnce.Do(func() {
		close(p.reconcileDone)
	})

	if interval <= 0 {
		interval = 30 * time.Second
	}
	if staleAfter <= 0 {
		staleAfter = time.Minute
	}
	if limit <= 0 {
		limit = 100
	}

	slog.Info("event reconciler started", "interval", interval, "stale_after", staleAfter, "limit", limit)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("event reconciler shutting down")
			return
		case <-ticker.C:
			p.requeueStalePending(ctx, staleAfter, limit)
		}
	}
}

func (p *Processor) requeueStalePending(ctx context.Context, staleAfter time.Duration, limit int) {
	events, err := p.repo.ListPendingOlderThan(ctx, staleAfter, limit)
	if err != nil {
		slog.Error("failed to list stale pending events", "error", err)
		return
	}

	for _, event := range events {
		if err := p.redisClient.LPush(ctx, queueKey, event.ID).Err(); err != nil {
			slog.Error("failed to requeue stale pending event", "event_id", event.ID, "error", err)
			continue
		}
		slog.Warn("requeued stale pending event", "event_id", event.ID, "updated_at", event.UpdatedAt)
	}
}

// Wait blocks until the worker loop exits or the wait context times out.
func (p *Processor) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitReconciler blocks until the reconciler loop exits or the wait context times out.
func (p *Processor) WaitReconciler(ctx context.Context) error {
	select {
	case <-p.reconcileDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processOne handles a single event: load, send to notification service, update status.
func (p *Processor) processOne(ctx context.Context, eventID string) {
	slog.Info("processing event", "event_id", eventID)

	// Load the event from the database.
	event, err := p.repo.FindByID(ctx, eventID)
	if err != nil {
		slog.Error("failed to load event", "event_id", eventID, "error", err)
		return
	}
	if event == nil {
		slog.Warn("event not found in database", "event_id", eventID)
		return
	}

	// Idempotent guard: skip already processed or failed events.
	if event.Status == "processed" || event.Status == "failed" {
		slog.Info("event already in terminal state, skipping", "event_id", eventID, "status", event.Status)
		return
	}

	// Send to notification service with retries.
	if err := p.sendToNotificationService(ctx, event); err != nil {
		slog.Error("failed to send notification after retries", "event_id", eventID, "error", err)
		if updateErr := p.repo.UpdateStatus(ctx, eventID, "failed"); updateErr != nil {
			slog.Error("failed to update event status to failed", "event_id", eventID, "error", updateErr)
		}
		return
	}

	// Mark as processed.
	if err := p.repo.UpdateStatus(ctx, eventID, "processed"); err != nil {
		slog.Error("failed to update event status to processed", "event_id", eventID, "error", err)
		return
	}

	slog.Info("event processed successfully", "event_id", eventID)
}

// sendToNotificationService sends the event to the notification service with retry logic.
func (p *Processor) sendToNotificationService(ctx context.Context, event *models.Event) error {
	body := map[string]interface{}{
		"event_id":   event.ID,
		"event_type": event.Type,
		"payload":    event.Payload,
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal notification body: %w", err)
	}

	url := p.notificationURL + "/notifications"

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s for default retryBase of 1s.
			delay := p.retryBaseDelay * (1 << (attempt - 1))
			slog.Info("retrying notification send",
				"event_id", event.ID,
				"attempt", attempt,
				"delay", delay,
			)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			lastErr = fmt.Errorf("create request: %w", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if event.RequestID != "" {
			req.Header.Set("X-Request-ID", event.RequestID)
		}

		resp, err := p.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http post: %w", err)
			slog.Warn("notification send failed (network)",
				"event_id", event.ID,
				"attempt", attempt,
				"error", err,
			)
			continue
		}

		// Read and close body to free connection.
		resp.Body.Close()

		// 2xx = success.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		// 4xx = permanent error, do NOT retry.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			slog.Error("notification service rejected request (permanent)",
				"event_id", event.ID,
				"status", resp.StatusCode,
			)
			return fmt.Errorf("permanent error: status %d", resp.StatusCode)
		}

		// 5xx = transient, retry.
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		slog.Warn("notification send failed (transient)",
			"event_id", event.ID,
			"attempt", attempt,
			"status", resp.StatusCode,
		)
	}

	return fmt.Errorf("failed after %d attempts: %w", p.maxRetries+1, lastErr)
}
