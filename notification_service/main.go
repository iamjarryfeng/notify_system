package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/iamjarryfeng/notify_system/notification_service/internal/channels"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/config"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/db"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/handlers"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/middleware"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/repository"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/service"
)

func main() {
	// Structured JSON logging.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Load configuration.
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Connect to PostgreSQL.
	slog.Info("connecting to postgres")
	dbConn, err := sqlx.Connect("postgres", cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer dbConn.Close()
	dbConn.SetMaxOpenConns(25)
	dbConn.SetMaxIdleConns(5)

	// Run migrations.
	if err := db.RunMigrations(dbConn, "migrations"); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// Build channel dispatchers.
	baseDispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	dispatchers := make(map[string]channels.Dispatcher, len(baseDispatchers))
	for name, dispatcher := range baseDispatchers {
		dispatchers[name] = channels.NewRetryingDispatcher(dispatcher, cfg.DispatchMaxRetries, cfg.DispatchRetryBaseDelay)
	}

	// Build dependency graph.
	router := service.NewRouter(dispatchers)
	repo := repository.NewPostgresNotificationRepository(dbConn)
	svc := service.NewNotificationService(repo, router, dispatchers)
	handler := handlers.NewNotificationHandler(svc)

	// Setup Gin router.
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.StructuredLogger())
	r.Use(gin.Recovery())

	// Register routes.
	r.GET("/health", healthHandler)
	r.GET("/ready", readinessHandler(dbConn))
	r.POST("/notifications", handler.SendNotification)
	r.GET("/notifications/:id", handler.GetNotification)
	r.GET("/notifications", handler.ListNotifications)

	// Graceful shutdown via context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	// Channel to capture server errors.
	errCh := make(chan error, 1)

	// Start HTTP server in a goroutine.
	slog.Info("starting http server", "port", cfg.Port)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for shutdown signal or server error.
	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err, ok := <-errCh:
		if ok && err != nil {
			slog.Error("http server error", "error", err)
		}
	}

	// Drain HTTP server with a timeout.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http server shutdown error", "error", err)
	}

	slog.Info("notification service stopped")
}

func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func readinessHandler(dbConn *sqlx.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 500*time.Millisecond)
		defer cancel()

		if err := dbConn.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "dependency": "postgres"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
