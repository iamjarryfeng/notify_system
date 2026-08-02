package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jarryfeng/notify_system/event_processor/internal/middleware"
	"github.com/jarryfeng/notify_system/event_processor/internal/models"
	"github.com/jarryfeng/notify_system/event_processor/internal/service"
)

// mockEventService implements service.EventService for handler tests.
type mockEventService struct {
	event         *models.Event
	err           error
	lastRequestID string
	lastEventID   string
	lastEventType string
	lastPayload   json.RawMessage
}

func (m *mockEventService) IngestEvent(ctx context.Context, requestID, eventID, eventType string, rawPayload json.RawMessage) (*models.Event, error) {
	m.lastRequestID = requestID
	m.lastEventID = eventID
	m.lastEventType = eventType
	m.lastPayload = rawPayload
	if m.err != nil {
		return nil, m.err
	}
	return m.event, nil
}

func (m *mockEventService) GetEvent(ctx context.Context, id string) (*models.Event, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.event, nil
}

func (m *mockEventService) ListEvents(ctx context.Context, status string, limit, offset int) ([]*models.Event, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.event != nil {
		return []*models.Event{m.event}, nil
	}
	return nil, nil
}

func setupTestRouter(handler *EventHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.POST("/events", handler.IngestEvent)
	r.GET("/events/:id", handler.GetEvent)
	r.GET("/events", handler.ListEvents)
	return r
}

func TestIngestEventHandler_Success(t *testing.T) {
	svc := &mockEventService{
		event: &models.Event{
			ID:     "test-uuid",
			Type:   "user.registered",
			Status: "pending",
		},
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", w.Code)
	}

	var resp models.Event
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.ID != "test-uuid" {
		t.Errorf("expected id 'test-uuid', got %q", resp.ID)
	}
	if svc.lastRequestID == "" {
		t.Fatal("expected request_id to be forwarded to service")
	}
}

func TestIngestEventHandler_ForwardsRequestIDHeader(t *testing.T) {
	svc := &mockEventService{
		event: &models.Event{ID: "test-uuid", Type: "user.registered", Status: "pending"},
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-from-client")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", w.Code)
	}
	if svc.lastRequestID != "req-from-client" {
		t.Fatalf("expected request_id req-from-client, got %q", svc.lastRequestID)
	}
}

func TestIngestEventHandler_ForwardsCustomEventID(t *testing.T) {
	svc := &mockEventService{
		event: &models.Event{ID: "550e8400-e29b-41d4-a716-446655440000", Type: "user.registered", Status: "pending"},
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"id":"550e8400-e29b-41d4-a716-446655440000","type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", w.Code)
	}
	if svc.lastEventID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("expected custom event id to be forwarded, got %q", svc.lastEventID)
	}
}

func TestIngestEventHandler_InvalidPayload(t *testing.T) {
	svc := &mockEventService{
		err: service.ErrInvalidPayload,
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"type":"user.registered","payload":[1,2,3]}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIngestEventHandler_EmptyType(t *testing.T) {
	svc := &mockEventService{
		err: service.ErrInvalidEventType,
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"type":"","payload":{}}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestIngestEventHandler_EnqueueFailure(t *testing.T) {
	svc := &mockEventService{
		err: service.ErrEnqueueFailed,
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestIngestEventHandler_InvalidEventID(t *testing.T) {
	svc := &mockEventService{
		err: service.ErrInvalidEventID,
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"id":"not-a-uuid","type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestIngestEventHandler_DuplicateEvent(t *testing.T) {
	svc := &mockEventService{
		err: service.ErrDuplicateEvent,
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	body := `{"id":"550e8400-e29b-41d4-a716-446655440000","type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

func TestGetEventHandler_Success(t *testing.T) {
	svc := &mockEventService{
		event: &models.Event{
			ID:     "test-uuid",
			Type:   "user.registered",
			Status: "processed",
		},
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	req, _ := http.NewRequest(http.MethodGet, "/events/test-uuid", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestGetEventHandler_NotFound(t *testing.T) {
	svc := &mockEventService{
		err: service.ErrEventNotFound,
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	req, _ := http.NewRequest(http.MethodGet, "/events/non-existent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestListEventsHandler_Success(t *testing.T) {
	svc := &mockEventService{
		event: &models.Event{
			ID:     "test-uuid",
			Type:   "user.registered",
			Status: "pending",
		},
	}
	handler := NewEventHandler(svc)
	router := setupTestRouter(handler)

	req, _ := http.NewRequest(http.MethodGet, "/events?status=pending", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestListEventsHandler_InvalidPagination(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "non numeric limit", path: "/events?limit=abc"},
		{name: "too large limit", path: "/events?limit=101"},
		{name: "negative offset", path: "/events?offset=-1"},
		{name: "non numeric offset", path: "/events?offset=abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEventHandler(&mockEventService{})
			router := setupTestRouter(handler)

			req, _ := http.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
