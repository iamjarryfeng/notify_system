package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jarryfeng/notify_system/event_processor/internal/models"
	"github.com/jarryfeng/notify_system/event_processor/internal/repository"
)

// mockEventRepository implements repository.EventRepository for testing.
type mockEventRepository struct {
	events  map[string]*models.Event
	counter int
}

func newMockEventRepository() *mockEventRepository {
	return &mockEventRepository{
		events: make(map[string]*models.Event),
	}
}

func (m *mockEventRepository) Save(ctx context.Context, event *models.Event) error {
	m.counter++
	if event.ID == "" {
		event.ID = fmt.Sprintf("test-id-%d", m.counter)
	} else if _, exists := m.events[event.ID]; exists {
		return repository.ErrDuplicateEvent
	}
	event.Status = "pending"
	m.events[event.ID] = event
	return nil
}

func (m *mockEventRepository) FindByID(ctx context.Context, id string) (*models.Event, error) {
	event, ok := m.events[id]
	if !ok {
		return nil, nil
	}
	return event, nil
}

func (m *mockEventRepository) List(ctx context.Context, status string, limit, offset int) ([]*models.Event, error) {
	var result []*models.Event
	for _, e := range m.events {
		if status == "" || e.Status == status {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *mockEventRepository) ListPendingOlderThan(ctx context.Context, olderThan time.Duration, limit int) ([]*models.Event, error) {
	return nil, nil
}

func (m *mockEventRepository) UpdateStatus(ctx context.Context, id, status string) error {
	event, ok := m.events[id]
	if ok {
		event.Status = status
	}
	return nil
}

// mockQueueClient implements QueueClient for testing.
type mockQueueClient struct {
	enqueuedIDs []string
	err         error
}

func (m *mockQueueClient) Enqueue(ctx context.Context, queueKey string, eventID string) error {
	if m.err != nil {
		return m.err
	}
	m.enqueuedIDs = append(m.enqueuedIDs, eventID)
	return nil
}

func TestIngestEvent(t *testing.T) {
	tests := []struct {
		name       string
		requestID  string
		eventID    string
		eventType  string
		rawPayload json.RawMessage
		wantErr    error
		wantStatus string
	}{
		{
			name:       "valid event with object payload",
			requestID:  "req-123",
			eventID:    "",
			eventType:  "user.registered",
			rawPayload: json.RawMessage(`{"email":"test@example.com","user_id":"abc-123"}`),
			wantErr:    nil,
			wantStatus: "pending",
		},
		{
			name:       "valid event with empty payload object",
			requestID:  "req-456",
			eventID:    "",
			eventType:  "user.registered",
			rawPayload: json.RawMessage(`{}`),
			wantErr:    nil,
			wantStatus: "pending",
		},
		{
			name:       "empty event type",
			requestID:  "req-empty-type",
			eventID:    "",
			eventType:  "",
			rawPayload: json.RawMessage(`{"email":"test@example.com"}`),
			wantErr:    ErrInvalidEventType,
		},
		{
			name:       "nil payload",
			requestID:  "req-nil-payload",
			eventID:    "",
			eventType:  "user.registered",
			rawPayload: nil,
			wantErr:    ErrInvalidPayload,
		},
		{
			name:       "empty payload",
			requestID:  "req-empty-payload",
			eventID:    "",
			eventType:  "user.registered",
			rawPayload: json.RawMessage(``),
			wantErr:    ErrInvalidPayload,
		},
		{
			name:       "array payload instead of object",
			requestID:  "req-array-payload",
			eventID:    "",
			eventType:  "user.registered",
			rawPayload: json.RawMessage(`[1,2,3]`),
			wantErr:    ErrInvalidPayload,
		},
		{
			name:       "string payload instead of object",
			requestID:  "req-string-payload",
			eventID:    "",
			eventType:  "user.registered",
			rawPayload: json.RawMessage(`"not an object"`),
			wantErr:    ErrInvalidPayload,
		},
		{
			name:       "json null payload",
			requestID:  "req-null-payload",
			eventID:    "",
			eventType:  "user.registered",
			rawPayload: json.RawMessage(`null`),
			wantErr:    ErrInvalidPayload,
		},
		{
			name:       "invalid custom event id",
			requestID:  "req-invalid-id",
			eventID:    "not-a-uuid",
			eventType:  "user.registered",
			rawPayload: json.RawMessage(`{"email":"test@example.com"}`),
			wantErr:    ErrInvalidEventID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockEventRepository()
			queue := &mockQueueClient{}
			svc := &eventService{repo: repo, queue: queue}

			event, err := svc.IngestEvent(context.Background(), tt.requestID, tt.eventID, tt.eventType, tt.rawPayload)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr.Error())
				}
				if !strings.Contains(err.Error(), tt.wantErr.Error()) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr.Error(), err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if event == nil {
				t.Fatal("expected event, got nil")
			}
			if event.Type != tt.eventType {
				t.Errorf("expected type %q, got %q", tt.eventType, event.Type)
			}
			if event.Status != tt.wantStatus {
				t.Errorf("expected status %q, got %q", tt.wantStatus, event.Status)
			}
			if event.ID == "" {
				t.Error("expected non-empty event ID")
			}
			if event.RequestID != tt.requestID {
				t.Errorf("expected request_id %q, got %q", tt.requestID, event.RequestID)
			}
			if tt.eventID != "" && event.ID != tt.eventID {
				t.Errorf("expected event id %q, got %q", tt.eventID, event.ID)
			}

			// Verify the event was enqueued.
			if len(queue.enqueuedIDs) != 1 || queue.enqueuedIDs[0] != event.ID {
				t.Error("expected event to be enqueued")
			}
		})
	}
}

func TestGetEvent(t *testing.T) {
	ctx := context.Background()
	repo := newMockEventRepository()
	queue := &mockQueueClient{}
	svc := &eventService{repo: repo, queue: queue}

	// Create and save an event.
	event, err := svc.IngestEvent(ctx, "req-lookup", "", "user.registered", json.RawMessage(`{"email":"test@example.com"}`))
	if err != nil {
		t.Fatalf("failed to create event: %v", err)
	}

	// Retrieve it.
	got, err := svc.GetEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected event, got nil")
	}
	if got.ID != event.ID {
		t.Errorf("expected id %q, got %q", event.ID, got.ID)
	}
}

func TestIngestEventEnqueueFailure(t *testing.T) {
	repo := newMockEventRepository()
	queue := &mockQueueClient{err: errors.New("redis unavailable")}
	svc := &eventService{repo: repo, queue: queue}

	_, err := svc.IngestEvent(context.Background(), "req-enqueue", "", "user.registered", json.RawMessage(`{"email":"test@example.com"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), ErrEnqueueFailed.Error()) {
		t.Fatalf("expected error containing %q, got %v", ErrEnqueueFailed, err)
	}
}

func TestGetEventNotFound(t *testing.T) {
	repo := newMockEventRepository()
	queue := &mockQueueClient{}
	svc := &eventService{repo: repo, queue: queue}

	_, err := svc.GetEvent(context.Background(), "non-existent-id")
	if err != ErrEventNotFound {
		t.Errorf("expected ErrEventNotFound, got %v", err)
	}
}

func TestListEvents(t *testing.T) {
	ctx := context.Background()
	repo := newMockEventRepository()
	queue := &mockQueueClient{}
	svc := &eventService{repo: repo, queue: queue}

	// Create two events.
	svc.IngestEvent(ctx, "req-a", "", "user.registered", json.RawMessage(`{"email":"a@b.com"}`))
	svc.IngestEvent(ctx, "req-b", "", "order.completed", json.RawMessage(`{"email":"c@d.com"}`))

	// List all.
	events, err := svc.ListEvents(ctx, "", 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
}

func TestIngestEventDuplicateID(t *testing.T) {
	repo := newMockEventRepository()
	queue := &mockQueueClient{}
	svc := &eventService{repo: repo, queue: queue}

	eventID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := svc.IngestEvent(context.Background(), "req-first", eventID, "user.registered", json.RawMessage(`{"email":"first@example.com"}`)); err != nil {
		t.Fatalf("unexpected first ingest error: %v", err)
	}

	_, err := svc.IngestEvent(context.Background(), "req-second", eventID, "user.registered", json.RawMessage(`{"email":"second@example.com"}`))
	if !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("expected ErrDuplicateEvent, got %v", err)
	}
}
