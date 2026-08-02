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

	miniredis "github.com/alicebob/miniredis/v2"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/iamjarryfeng/notify_system/event_processor/internal/config"
	internaldb "github.com/iamjarryfeng/notify_system/event_processor/internal/db"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/handlers"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/middleware"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/models"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/repository"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/service"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/worker"
)

type downstreamNotificationRequest struct {
	EventID   string                 `json:"event_id"`
	EventType string                 `json:"event_type"`
	Payload   map[string]interface{} `json:"payload"`
	RequestID string                 `json:"-"`
}

func TestEventProcessorEndToEndFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	port := reserveIntegrationPort(t)
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

	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		rdb.Close()
	})

	downstreamCh := make(chan downstreamNotificationRequest, 1)
	notificationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload downstreamNotificationRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode downstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payload.RequestID = r.Header.Get("X-Request-ID")
		select {
		case downstreamCh <- payload:
		default:
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer notificationServer.Close()

	repo := repository.NewPostgresEventRepository(dbConn)
	svc := service.NewEventService(repo, rdb)
	handler := handlers.NewEventHandler(svc)
	proc := worker.NewProcessor(repo, rdb, &config.Config{
		NotificationServiceURL: notificationServer.URL,
		MaxRetries:             0,
		RetryBaseDelay:         time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go proc.Run(ctx)
	t.Cleanup(func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer waitCancel()
		if err := proc.Wait(waitCtx); err != nil {
			t.Fatalf("wait for worker shutdown: %v", err)
		}
	})

	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(gin.Recovery())
	router.POST("/events", handler.IngestEvent)
	router.GET("/events/:id", handler.GetEvent)

	eventID := "550e8400-e29b-41d4-a716-446655440000"
	requestBody := `{"id":"` + eventID + `","type":"user.registered","payload":{"email":"flow@example.com","user_id":"abc-123"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "flow-request-id")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("X-Request-ID"); got != "flow-request-id" {
		t.Fatalf("expected response X-Request-ID flow-request-id, got %q", got)
	}

	var created models.Event
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal ingest response: %v", err)
	}
	if created.Status != "pending" {
		t.Fatalf("expected initial status pending, got %q", created.Status)
	}
	if created.ID != eventID {
		t.Fatalf("expected created id %q, got %q", eventID, created.ID)
	}
	if created.RequestID != "flow-request-id" {
		t.Fatalf("expected persisted request_id flow-request-id, got %q", created.RequestID)
	}

	var downstream downstreamNotificationRequest
	select {
	case downstream = <-downstreamCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for downstream notification request")
	}

	if downstream.EventID != created.ID {
		t.Fatalf("expected downstream event_id %q, got %q", created.ID, downstream.EventID)
	}
	if downstream.EventType != "user.registered" {
		t.Fatalf("expected downstream event_type user.registered, got %q", downstream.EventType)
	}
	if downstream.RequestID != "flow-request-id" {
		t.Fatalf("expected downstream X-Request-ID flow-request-id, got %q", downstream.RequestID)
	}
	if downstream.Payload["email"] != "flow@example.com" {
		t.Fatalf("expected downstream payload email flow@example.com, got %#v", downstream.Payload)
	}

	if err := waitForCondition(5*time.Second, func() error {
		event, findErr := repo.FindByID(context.Background(), created.ID)
		if findErr != nil {
			return findErr
		}
		if event == nil {
			return fmt.Errorf("event %s not found", created.ID)
		}
		if event.Status != "processed" {
			return fmt.Errorf("event status is %s", event.Status)
		}
		if event.RequestID != "flow-request-id" {
			return fmt.Errorf("event request_id is %s", event.RequestID)
		}
		return nil
	}); err != nil {
		t.Fatalf("wait for processed event: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/events/"+created.ID, nil)
	getReq.Header.Set("X-Request-ID", "verify-request-id")
	getResp := httptest.NewRecorder()
	router.ServeHTTP(getResp, getReq)

	if getResp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", getResp.Code, getResp.Body.String())
	}
	var fetched models.Event
	if err := json.Unmarshal(getResp.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("unmarshal get event response: %v", err)
	}
	if fetched.Status != "processed" {
		t.Fatalf("expected fetched status processed, got %q", fetched.Status)
	}
	if fetched.RequestID != "flow-request-id" {
		t.Fatalf("expected fetched request_id flow-request-id, got %q", fetched.RequestID)
	}

	duplicateReq := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(requestBody))
	duplicateReq.Header.Set("Content-Type", "application/json")
	duplicateReq.Header.Set("X-Request-ID", "duplicate-request-id")
	duplicateResp := httptest.NewRecorder()
	router.ServeHTTP(duplicateResp, duplicateReq)

	if duplicateResp.Code != http.StatusConflict {
		t.Fatalf("expected duplicate status 409, got %d: %s", duplicateResp.Code, duplicateResp.Body.String())
	}
	select {
	case unexpected := <-downstreamCh:
		t.Fatalf("unexpected second downstream dispatch: %#v", unexpected)
	case <-time.After(200 * time.Millisecond):
	}
}

func handleIntegrationDependencyError(t *testing.T, dependency string, err error) {
	t.Helper()
	if os.Getenv("RUN_INTEGRATION") == "1" {
		t.Fatalf("%s unavailable: %v", dependency, err)
	}
	t.Skipf("%s unavailable: %v", dependency, err)
}

func reserveIntegrationPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForCondition(timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("condition not met before timeout")
	}
	return lastErr
}
