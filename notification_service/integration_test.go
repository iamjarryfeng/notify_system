package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/iamjarryfeng/notify_system/notification_service/internal/channels"
	internaldb "github.com/iamjarryfeng/notify_system/notification_service/internal/db"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/handlers"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/middleware"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/models"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/repository"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/service"
)

type sendNotificationResponse struct {
	Notifications []*models.Notification `json:"notifications"`
}

type listNotificationsResponse struct {
	Notifications []*models.Notification `json:"notifications"`
}

func TestNotificationHTTPAndDatabaseIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	port := reservePort(t)
	tmpDir := t.TempDir()
	pgConfig := embeddedpostgres.DefaultConfig().
		Port(uint32(port)).
		Database("notify").
		Username("notify").
		Password("notify").
		Version(embeddedpostgres.V16).
		RuntimePath(filepath.Join(tmpDir, "runtime")).
		DataPath(filepath.Join(tmpDir, "data")).
		CachePath(filepath.Join(tmpDir, "cache")).
		StartTimeout(45 * time.Second)

	postgres := embeddedpostgres.NewDatabase(pgConfig)
	if err := postgres.Start(); err != nil {
		handleIntegrationDependencyError(t, "embedded postgres", err)
	}
	t.Cleanup(func() {
		if err := postgres.Stop(); err != nil {
			t.Fatalf("stop embedded postgres: %v", err)
		}
	})

	dbConn, err := sqlx.Connect("postgres", pgConfig.GetConnectionURL()+"?sslmode=disable")
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(func() {
		dbConn.Close()
	})

	if _, err := dbConn.Exec(`CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatalf("create pgcrypto extension: %v", err)
	}
	if err := internaldb.RunMigrations(dbConn, "migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	engine := newIntegrationRouter(dbConn)
	eventID := uuid.NewString()
	body := fmt.Sprintf(`{"event_id":"%s","event_type":"user.registered","payload":{"email":"integration@example.com"}}`, eventID)
	req := httptest.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "integration-request-id")
	postResp := httptest.NewRecorder()

	engine.ServeHTTP(postResp, req)

	if postResp.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", postResp.Code, postResp.Body.String())
	}
	if got := postResp.Header().Get("X-Request-ID"); got != "integration-request-id" {
		t.Fatalf("expected response X-Request-ID integration-request-id, got %q", got)
	}

	var created sendNotificationResponse
	if err := json.Unmarshal(postResp.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal post response: %v", err)
	}
	if len(created.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(created.Notifications))
	}
	createdID := created.Notifications[0].ID

	duplicateReq := httptest.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	duplicateReq.Header.Set("Content-Type", "application/json")
	duplicateReq.Header.Set("X-Request-ID", "integration-request-id-duplicate")
	duplicateResp := httptest.NewRecorder()
	engine.ServeHTTP(duplicateResp, duplicateReq)
	if duplicateResp.Code != http.StatusCreated {
		t.Fatalf("expected duplicate status 201, got %d: %s", duplicateResp.Code, duplicateResp.Body.String())
	}
	var duplicate sendNotificationResponse
	if err := json.Unmarshal(duplicateResp.Body.Bytes(), &duplicate); err != nil {
		t.Fatalf("unmarshal duplicate response: %v", err)
	}
	if len(duplicate.Notifications) != 1 {
		t.Fatalf("expected 1 duplicate notification, got %d", len(duplicate.Notifications))
	}
	if duplicate.Notifications[0].ID != createdID {
		t.Fatalf("expected duplicate notification id %q, got %q", createdID, duplicate.Notifications[0].ID)
	}

	var persisted models.Notification
	if err := dbConn.GetContext(context.Background(), &persisted, `
		SELECT id, event_id, channel, recipient, message, status, sent_at, created_at
		FROM notifications WHERE id = $1`, createdID); err != nil {
		t.Fatalf("query persisted notification: %v", err)
	}
	if persisted.EventID != eventID {
		t.Fatalf("expected persisted event_id %q, got %q", eventID, persisted.EventID)
	}
	if persisted.Channel != "email" {
		t.Fatalf("expected persisted channel email, got %q", persisted.Channel)
	}
	if persisted.Status != "sent" {
		t.Fatalf("expected persisted status sent, got %q", persisted.Status)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/notifications?event_id="+eventID, nil)
	listReq.Header.Set("X-Request-ID", "integration-request-id-list")
	listResp := httptest.NewRecorder()
	engine.ServeHTTP(listResp, listReq)

	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d: %s", listResp.Code, listResp.Body.String())
	}
	var listed listNotificationsResponse
	if err := json.Unmarshal(listResp.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listed.Notifications) != 1 {
		t.Fatalf("expected 1 listed notification, got %d", len(listed.Notifications))
	}
	if listed.Notifications[0].EventID != eventID {
		t.Fatalf("expected listed event_id %q, got %q", eventID, listed.Notifications[0].EventID)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/notifications/"+createdID, nil)
	getReq.Header.Set("X-Request-ID", "integration-request-id-2")
	getResp := httptest.NewRecorder()
	engine.ServeHTTP(getResp, getReq)

	if getResp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", getResp.Code, getResp.Body.String())
	}
	var fetched models.Notification
	if err := json.Unmarshal(getResp.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if fetched.ID != createdID {
		t.Fatalf("expected fetched notification id %q, got %q", createdID, fetched.ID)
	}
	if fetched.Recipient != "integration@example.com" {
		t.Fatalf("expected recipient integration@example.com, got %q", fetched.Recipient)
	}
	if got := getResp.Header().Get("X-Request-ID"); got != "integration-request-id-2" {
		t.Fatalf("expected GET response X-Request-ID integration-request-id-2, got %q", got)
	}
}

func handleIntegrationDependencyError(t *testing.T, dependency string, err error) {
	t.Helper()
	if os.Getenv("RUN_INTEGRATION") == "1" {
		t.Fatalf("%s unavailable: %v", dependency, err)
	}
	t.Skipf("%s unavailable: %v", dependency, err)
}

func newIntegrationRouter(dbConn *sqlx.DB) *gin.Engine {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := service.NewRouter(dispatchers)
	repo := repository.NewPostgresNotificationRepository(dbConn)
	svc := service.NewNotificationService(repo, router, dispatchers)
	handler := handlers.NewNotificationHandler(svc)

	engine := gin.New()
	engine.Use(middleware.RequestID())
	engine.Use(gin.Recovery())
	engine.POST("/notifications", handler.SendNotification)
	engine.GET("/notifications", handler.ListNotifications)
	engine.GET("/notifications/:id", handler.GetNotification)
	return engine
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
