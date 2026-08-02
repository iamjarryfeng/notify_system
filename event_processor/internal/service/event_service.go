package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/jarryfeng/notify_system/event_processor/internal/models"
	"github.com/jarryfeng/notify_system/event_processor/internal/repository"
)

// Sentinel errors for business logic.
var (
	ErrInvalidEventType = errors.New("event type is required and must be a non-empty string")
	ErrInvalidEventID   = errors.New("event id must be a valid UUID")
	ErrInvalidPayload   = errors.New("payload must be a non-nil JSON object")
	ErrEventNotFound    = errors.New("event not found")
	ErrDuplicateEvent   = errors.New("event with the same id already exists")
	ErrEnqueueFailed    = errors.New("event was persisted but could not be enqueued for processing")
)

// EventService defines the business logic contract for events.
type EventService interface {
	IngestEvent(ctx context.Context, requestID, eventID, eventType string, rawPayload json.RawMessage) (*models.Event, error)
	GetEvent(ctx context.Context, id string) (*models.Event, error)
	ListEvents(ctx context.Context, status string, limit, offset int) ([]*models.Event, error)
}

// QueueClient abstracts queue operations for testability.
type QueueClient interface {
	Enqueue(ctx context.Context, queueKey string, eventID string) error
}

// redisQueueClient adapts *redis.Client to the QueueClient interface.
type redisQueueClient struct {
	client *redis.Client
}

func (r *redisQueueClient) Enqueue(ctx context.Context, queueKey string, eventID string) error {
	return r.client.LPush(ctx, queueKey, eventID).Err()
}

type eventService struct {
	repo   repository.EventRepository
	queue  QueueClient
}

// NewEventService constructs an EventService.
func NewEventService(repo repository.EventRepository, rdb *redis.Client) EventService {
	return &eventService{
		repo:  repo,
		queue: &redisQueueClient{client: rdb},
	}
}

// IngestEvent validates the event, persists it, and enqueues it for processing.
func (s *eventService) IngestEvent(ctx context.Context, requestID, eventID, eventType string, rawPayload json.RawMessage) (*models.Event, error) {
	logAttrs := []any{"request_id", requestID}
	if eventID != "" {
		if _, err := uuid.Parse(eventID); err != nil {
			return nil, ErrInvalidEventID
		}
	}

	// Validate type is non-empty.
	if eventType == "" {
		return nil, ErrInvalidEventType
	}

	// Validate payload is a JSON object (not null, not scalar, not array).
	if rawPayload == nil || len(rawPayload) == 0 {
		return nil, ErrInvalidPayload
	}
	var payloadMap map[string]interface{}
	if err := json.Unmarshal(rawPayload, &payloadMap); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	// After unmarshal, check if payload was actually an object vs array/scalar.
	if payloadMap == nil {
		return nil, ErrInvalidPayload
	}

	event := &models.Event{
		ID:        eventID,
		RequestID: requestID,
		Type:      eventType,
		Payload:   payloadMap,
	}

	// Persist to database.
	if err := s.repo.Save(ctx, event); err != nil {
		if errors.Is(err, repository.ErrDuplicateEvent) {
			return nil, ErrDuplicateEvent
		}
		return nil, fmt.Errorf("persist event: %w", err)
	}

	if err := s.queue.Enqueue(ctx, "events:queue", event.ID); err != nil {
		slog.Error("failed to enqueue event to redis", append(logAttrs, "event_id", event.ID, "error", err)...)
		return nil, fmt.Errorf("%w: %v", ErrEnqueueFailed, err)
	}
	slog.Info("event enqueued", append(logAttrs, "event_id", event.ID, "type", event.Type)...)

	return event, nil
}

// GetEvent retrieves a single event by ID.
func (s *eventService) GetEvent(ctx context.Context, id string) (*models.Event, error) {
	event, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get event: %w", err)
	}
	if event == nil {
		return nil, ErrEventNotFound
	}
	return event, nil
}

// ListEvents returns events, optionally filtered by status, with pagination.
func (s *eventService) ListEvents(ctx context.Context, status string, limit, offset int) ([]*models.Event, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.List(ctx, status, limit, offset)
}
