package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/config"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/models"
)

type stubEventRepository struct {
	mu          sync.Mutex
	events      map[string]*models.Event
	statusCalls []string
	staleEvents []*models.Event
}

func newStubEventRepository() *stubEventRepository {
	return &stubEventRepository{
		events: make(map[string]*models.Event),
	}
}

func (r *stubEventRepository) Save(ctx context.Context, event *models.Event) error {
	panic("unexpected Save call")
}

func (r *stubEventRepository) FindByID(ctx context.Context, id string) (*models.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if event, ok := r.events[id]; ok {
		clone := *event
		return &clone, nil
	}
	return nil, nil
}

func (r *stubEventRepository) List(ctx context.Context, status string, limit, offset int) ([]*models.Event, error) {
	panic("unexpected List call")
}

func (r *stubEventRepository) ListPendingOlderThan(ctx context.Context, olderThan time.Duration, limit int) ([]*models.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*models.Event, 0, len(r.staleEvents))
	for _, event := range r.staleEvents {
		clone := *event
		result = append(result, &clone)
	}
	return result, nil
}

func (r *stubEventRepository) UpdateStatus(ctx context.Context, id, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusCalls = append(r.statusCalls, fmt.Sprintf("%s:%s", id, status))
	if event, ok := r.events[id]; ok {
		event.Status = status
	}
	return nil
}

func TestRequeueStalePendingEvents(t *testing.T) {
	repo := newStubEventRepository()
	repo.staleEvents = []*models.Event{
		{ID: "evt-stale", Status: "pending", UpdatedAt: time.Now().Add(-2 * time.Minute)},
	}

	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		rdb.Close()
	})

	processor := NewProcessor(repo, rdb, &config.Config{
		NotificationServiceURL: "http://notification-service",
		MaxRetries:             0,
		RetryBaseDelay:         time.Millisecond,
	})

	processor.requeueStalePending(context.Background(), time.Minute, 100)

	result, err := rdb.LPop(context.Background(), queueKey).Result()
	if err != nil {
		t.Fatalf("expected requeued event id, got error: %v", err)
	}
	if result != "evt-stale" {
		t.Fatalf("expected evt-stale, got %q", result)
	}
}

func TestProcessOneIgnoresParentCancellationForInFlightWork(t *testing.T) {
	repo := newStubEventRepository()
	repo.events["evt-1"] = &models.Event{
		ID:        "evt-1",
		RequestID: "req-evt-1",
		Type:      "user.registered",
		Status:    "pending",
		Payload:   map[string]interface{}{"email": "test@example.com"},
	}

	requestStarted := make(chan struct{})
	requestHeaders := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestHeaders <- r.Header.Get("X-Request-ID")
		close(requestStarted)
		<-time.After(25 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	processor := NewProcessor(repo, redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"}), &config.Config{
		NotificationServiceURL: server.URL,
		MaxRetries:             0,
		RetryBaseDelay:         time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	workDone := make(chan struct{})
	go func() {
		defer close(workDone)
		processor.processOne(context.WithoutCancel(ctx), "evt-1")
	}()

	<-requestStarted
	cancel()

	select {
	case <-workDone:
	case <-time.After(time.Second):
		t.Fatal("processOne did not finish after parent cancellation")
	}

	select {
	case requestID := <-requestHeaders:
		if requestID != "req-evt-1" {
			t.Fatalf("expected downstream X-Request-ID req-evt-1, got %q", requestID)
		}
	default:
		t.Fatal("expected downstream request to capture X-Request-ID")
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if got := repo.events["evt-1"].Status; got != "processed" {
		t.Fatalf("expected event status processed, got %q", got)
	}
	if len(repo.statusCalls) != 1 || repo.statusCalls[0] != "evt-1:processed" {
		t.Fatalf("expected processed status update, got %#v", repo.statusCalls)
	}
	if repo.events["evt-1"].RequestID != "req-evt-1" {
		t.Fatalf("expected request_id to remain on event, got %q", repo.events["evt-1"].RequestID)
	}
}
