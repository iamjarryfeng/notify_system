package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jarryfeng/notify_system/notification_service/internal/channels"
	"github.com/jarryfeng/notify_system/notification_service/internal/models"
)

// mockNotificationRepository implements repository.NotificationRepository for testing.
type mockNotificationRepository struct {
	notifications map[string]*models.Notification
	counter       int
	savedStatuses []string
}

type transientFailingDispatcher struct {
	name         string
	failuresLeft int
	attempts     int
}

type alwaysFailingDispatcher struct {
	name string
}

func (d *transientFailingDispatcher) Name() string { return d.name }

func (d *transientFailingDispatcher) Send(ctx context.Context, recipient, message string) error {
	d.attempts++
	if d.failuresLeft > 0 {
		d.failuresLeft--
		return errors.New("temporary dispatch failure")
	}
	return nil
}

func (d *alwaysFailingDispatcher) Name() string { return d.name }

func (d *alwaysFailingDispatcher) Send(ctx context.Context, recipient, message string) error {
	return errors.New("dispatch failed")
}

func newMockNotificationRepository() *mockNotificationRepository {
	return &mockNotificationRepository{
		notifications: make(map[string]*models.Notification),
	}
}

func (m *mockNotificationRepository) Save(ctx context.Context, n *models.Notification) error {
	for _, existing := range m.notifications {
		if existing.EventID == n.EventID && existing.Channel == n.Channel {
			n.ID = existing.ID
			n.Status = existing.Status
			n.SentAt = existing.SentAt
			n.CreatedAt = existing.CreatedAt
			n.Inserted = false
			return nil
		}
	}

	m.counter++
	if n.ID == "" {
		n.ID = fmt.Sprintf("notif-%d", m.counter)
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	n.Inserted = true
	m.savedStatuses = append(m.savedStatuses, n.Status)
	m.notifications[n.ID] = n
	return nil
}

func (m *mockNotificationRepository) SaveAll(ctx context.Context, notifications []*models.Notification) error {
	for _, notification := range notifications {
		if err := m.Save(ctx, notification); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockNotificationRepository) FindByID(ctx context.Context, id string) (*models.Notification, error) {
	n, ok := m.notifications[id]
	if !ok {
		return nil, nil
	}
	return n, nil
}

func (m *mockNotificationRepository) List(ctx context.Context, eventID, status string, limit, offset int) ([]*models.Notification, error) {
	var result []*models.Notification
	for _, n := range m.notifications {
		if (eventID == "" || n.EventID == eventID) &&
			(status == "" || n.Status == status) {
			result = append(result, n)
		}
	}
	return result, nil
}

func (m *mockNotificationRepository) UpdateStatus(ctx context.Context, id, status string, sentAt *time.Time) error {
	n, ok := m.notifications[id]
	if ok {
		n.Status = status
		n.SentAt = sentAt
	}
	return nil
}

func TestSendNotificationSingleChannel(t *testing.T) {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)
	repo := newMockNotificationRepository()
	svc := &notificationService{repo: repo, router: router, dispatchers: dispatchers}

	payload := map[string]interface{}{"email": "test@example.com"}
	notifications, err := svc.SendNotification(context.Background(), "evt-001", "user.registered", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].Channel != "email" {
		t.Errorf("expected channel 'email', got %q", notifications[0].Channel)
	}
	if notifications[0].Status != "sent" {
		t.Errorf("expected status 'sent', got %q", notifications[0].Status)
	}
	if notifications[0].SentAt == nil {
		t.Error("expected sent_at to be set for successful notification")
	}
	if len(repo.savedStatuses) != 1 || repo.savedStatuses[0] != "pending" {
		t.Fatalf("expected notification to be saved as pending before dispatch, got %#v", repo.savedStatuses)
	}
}

func TestSendNotificationMultiChannel(t *testing.T) {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)
	repo := newMockNotificationRepository()
	svc := &notificationService{repo: repo, router: router, dispatchers: dispatchers}

	payload := map[string]interface{}{
		"email":       "test@example.com",
		"webhook_url": "https://hook.example.com",
	}
	notifications, err := svc.SendNotification(context.Background(), "evt-002", "order.completed", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifications))
	}

	channels := map[string]bool{}
	for _, n := range notifications {
		channels[n.Channel] = true
	}
	if !channels["email"] || !channels["webhook"] {
		t.Error("expected both email and webhook notifications")
	}
}

func TestSendNotificationMissingRecipient(t *testing.T) {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)
	repo := newMockNotificationRepository()
	svc := &notificationService{repo: repo, router: router, dispatchers: dispatchers}

	// user.registered without email should result in skipped route.
	payload := map[string]interface{}{"user_id": "no-email"}
	notifications, err := svc.SendNotification(context.Background(), "evt-003", "user.registered", payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), ErrInvalidNotification.Error()) {
		t.Fatalf("expected error containing %q, got %v", ErrInvalidNotification, err)
	}
	if len(notifications) != 0 {
		t.Errorf("expected 0 notifications, got %d", len(notifications))
	}
}

func TestSendNotificationRetriesDispatcher(t *testing.T) {
	flakyEmail := &transientFailingDispatcher{name: "email", failuresLeft: 2}
	dispatchers := map[string]channels.Dispatcher{
		"email":   channels.NewRetryingDispatcher(flakyEmail, 3, time.Millisecond),
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)
	repo := newMockNotificationRepository()
	svc := &notificationService{repo: repo, router: router, dispatchers: dispatchers}

	payload := map[string]interface{}{"email": "test@example.com"}
	notifications, err := svc.SendNotification(context.Background(), "evt-retry", "user.registered", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].Status != "sent" {
		t.Fatalf("expected sent notification after retries, got %q", notifications[0].Status)
	}
	if flakyEmail.attempts != 3 {
		t.Fatalf("expected 3 dispatch attempts, got %d", flakyEmail.attempts)
	}
}

func TestSendNotificationRecordsFailedDispatch(t *testing.T) {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &alwaysFailingDispatcher{name: "email"},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)
	repo := newMockNotificationRepository()
	svc := &notificationService{repo: repo, router: router, dispatchers: dispatchers}

	payload := map[string]interface{}{"email": "test@example.com"}
	notifications, err := svc.SendNotification(context.Background(), "evt-failed", "user.registered", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].Status != "failed" {
		t.Fatalf("expected failed notification, got %q", notifications[0].Status)
	}
	if notifications[0].SentAt != nil {
		t.Fatal("expected sent_at to remain nil for failed notification")
	}
	if len(repo.savedStatuses) != 1 || repo.savedStatuses[0] != "pending" {
		t.Fatalf("expected notification to be saved as pending before dispatch, got %#v", repo.savedStatuses)
	}
}

func TestSendNotificationSkipsDuplicateDispatch(t *testing.T) {
	email := &transientFailingDispatcher{name: "email"}
	dispatchers := map[string]channels.Dispatcher{
		"email":   email,
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)
	repo := newMockNotificationRepository()
	svc := &notificationService{repo: repo, router: router, dispatchers: dispatchers}

	payload := map[string]interface{}{"email": "test@example.com"}
	first, err := svc.SendNotification(context.Background(), "evt-idempotent", "user.registered", payload)
	if err != nil {
		t.Fatalf("first send unexpected error: %v", err)
	}
	second, err := svc.SendNotification(context.Background(), "evt-idempotent", "user.registered", payload)
	if err != nil {
		t.Fatalf("second send unexpected error: %v", err)
	}
	if email.attempts != 1 {
		t.Fatalf("expected dispatcher to be called once, got %d", email.attempts)
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("expected duplicate call to return existing notification id %q, got %q", first[0].ID, second[0].ID)
	}
	if second[0].Status != "sent" {
		t.Fatalf("expected duplicate call to return sent status, got %q", second[0].Status)
	}
}

func TestGetNotification(t *testing.T) {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)
	repo := newMockNotificationRepository()
	svc := &notificationService{repo: repo, router: router, dispatchers: dispatchers}

	payload := map[string]interface{}{"email": "test@example.com"}
	created, err := svc.SendNotification(context.Background(), "evt-004", "user.registered", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := svc.GetNotification(context.Background(), created[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != created[0].ID {
		t.Errorf("expected id %q, got %q", created[0].ID, got.ID)
	}
}

func TestGetNotificationNotFound(t *testing.T) {
	svc := &notificationService{repo: newMockNotificationRepository()}
	_, err := svc.GetNotification(context.Background(), "non-existent")
	if err != ErrNotificationNotFound {
		t.Errorf("expected ErrNotificationNotFound, got %v", err)
	}
}
