package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	internaldb "github.com/iamjarryfeng/notify_system/notification_service/internal/db"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/models"
)

func TestNotificationHTTPAndDatabaseIntegrationWithTestcontainers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("notify"),
		tcpostgres.WithUsername("notify"),
		tcpostgres.WithPassword("notify"),
	)
	if err != nil {
		handleIntegrationDependencyError(t, "testcontainers postgres", err)
	}
	t.Cleanup(func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if termErr := pgContainer.Terminate(terminateCtx); termErr != nil {
			t.Fatalf("terminate postgres container: %v", termErr)
		}
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	dbConn, err := connectWithRetry(ctx, connStr)
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
	body := fmt.Sprintf(`{"event_id":"%s","event_type":"user.registered","payload":{"email":"tc@example.com"}}`, eventID)
	req := httptest.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "testcontainers-request-id")
	postResp := httptest.NewRecorder()
	engine.ServeHTTP(postResp, req)

	if postResp.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", postResp.Code, postResp.Body.String())
	}
	if got := postResp.Header().Get("X-Request-ID"); got != "testcontainers-request-id" {
		t.Fatalf("expected response X-Request-ID testcontainers-request-id, got %q", got)
	}

	var created sendNotificationResponse
	if err := json.Unmarshal(postResp.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal post response: %v", err)
	}
	if len(created.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(created.Notifications))
	}
	createdID := created.Notifications[0].ID

	listReq := httptest.NewRequest(http.MethodGet, "/notifications?event_id="+eventID, nil)
	listReq.Header.Set("X-Request-ID", "testcontainers-list-id")
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

	var persisted models.Notification
	if err := dbConn.GetContext(context.Background(), &persisted, `
		SELECT id, event_id, channel, recipient, message, status, sent_at, created_at
		FROM notifications WHERE id = $1`, createdID); err != nil {
		t.Fatalf("query persisted notification: %v", err)
	}
	if persisted.EventID != eventID {
		t.Fatalf("expected persisted event_id %q, got %q", eventID, persisted.EventID)
	}
	if persisted.Recipient != "tc@example.com" {
		t.Fatalf("expected recipient tc@example.com, got %q", persisted.Recipient)
	}
	if persisted.Status != "sent" {
		t.Fatalf("expected persisted status sent, got %q", persisted.Status)
	}
}

func connectWithRetry(ctx context.Context, connStr string) (*sqlx.DB, error) {
	var lastErr error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		dbConn, err := sqlx.Connect("postgres", connStr)
		if err == nil {
			return dbConn, nil
		}
		lastErr = err
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}
