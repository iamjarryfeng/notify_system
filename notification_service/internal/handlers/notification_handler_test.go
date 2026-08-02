package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/models"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/service"
)

// mockNotificationService implements service.NotificationService for handler tests.
type mockNotificationService struct {
	notifications []*models.Notification
	err           error
}

func (m *mockNotificationService) SendNotification(ctx context.Context, eventID, eventType string, payload map[string]interface{}) ([]*models.Notification, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.notifications, nil
}

func (m *mockNotificationService) GetNotification(ctx context.Context, id string) (*models.Notification, error) {
	if m.err != nil {
		return nil, m.err
	}
	if len(m.notifications) > 0 {
		return m.notifications[0], nil
	}
	return nil, nil
}

func (m *mockNotificationService) ListNotifications(ctx context.Context, eventID, status string, limit, offset int) ([]*models.Notification, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.notifications, nil
}

func setupTestRouter(handler *NotificationHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/notifications", handler.SendNotification)
	r.GET("/notifications/:id", handler.GetNotification)
	r.GET("/notifications", handler.ListNotifications)
	return r
}

func TestSendNotificationHandler_Success(t *testing.T) {
	now := time.Now()
	svc := &mockNotificationService{
		notifications: []*models.Notification{
			{
				ID:        "notif-001",
				EventID:   "evt-001",
				Channel:   "email",
				Recipient: "test@example.com",
				Message:   "Welcome!",
				Status:    "sent",
				SentAt:    &now,
			},
		},
	}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	body := `{"event_id":"550e8400-e29b-41d4-a716-446655440000","event_type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendNotificationHandler_MissingEventID(t *testing.T) {
	svc := &mockNotificationService{}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	body := `{"event_type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestSendNotificationHandler_MissingEventType(t *testing.T) {
	svc := &mockNotificationService{}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	body := `{"event_id":"550e8400-e29b-41d4-a716-446655440000","payload":{}}`
	req, _ := http.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestSendNotificationHandler_InvalidEventID(t *testing.T) {
	svc := &mockNotificationService{}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	body := `{"event_id":"not-a-uuid","event_type":"user.registered","payload":{"email":"test@example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestSendNotificationHandler_InvalidRecipientPayload(t *testing.T) {
	svc := &mockNotificationService{
		err: service.ErrInvalidNotification,
	}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	body := `{"event_id":"550e8400-e29b-41d4-a716-446655440000","event_type":"user.registered","payload":{"user_id":"missing-email"}}`
	req, _ := http.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestSendNotificationHandler_NoRoutes(t *testing.T) {
	svc := &mockNotificationService{
		err: service.ErrNoRoutes,
	}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	body := `{"event_id":"550e8400-e29b-41d4-a716-446655440000","event_type":"unroutable.event","payload":{"webhook_url":"https://hook.example.com"}}`
	req, _ := http.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected status 422, got %d", w.Code)
	}
}

func TestGetNotificationHandler_Success(t *testing.T) {
	now := time.Now()
	svc := &mockNotificationService{
		notifications: []*models.Notification{
			{
				ID:      "notif-001",
				EventID: "evt-001",
				Channel: "email",
				Status:  "sent",
				SentAt:  &now,
			},
		},
	}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	req, _ := http.NewRequest(http.MethodGet, "/notifications/notif-001", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestGetNotificationHandler_NotFound(t *testing.T) {
	svc := &mockNotificationService{
		err: service.ErrNotificationNotFound,
	}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	req, _ := http.NewRequest(http.MethodGet, "/notifications/non-existent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestListNotificationsHandler_Success(t *testing.T) {
	svc := &mockNotificationService{
		notifications: []*models.Notification{},
	}
	handler := NewNotificationHandler(svc)
	router := setupTestRouter(handler)

	req, _ := http.NewRequest(http.MethodGet, "/notifications?event_id=550e8400-e29b-41d4-a716-446655440000", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if _, ok := resp["notifications"]; !ok {
		t.Error("expected 'notifications' key in response")
	}
}

func TestListNotificationsHandler_InvalidPagination(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "non numeric limit", path: "/notifications?limit=abc"},
		{name: "too large limit", path: "/notifications?limit=101"},
		{name: "negative offset", path: "/notifications?offset=-1"},
		{name: "non numeric offset", path: "/notifications?offset=abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewNotificationHandler(&mockNotificationService{})
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
