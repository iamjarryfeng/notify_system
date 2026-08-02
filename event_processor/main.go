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
	"github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/iamjarryfeng/notify_system/event_processor/internal/config"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/db"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/handlers"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/middleware"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/repository"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/service"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/worker"
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

	// Connect to Redis.
	slog.Info("connecting to redis")
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		slog.Error("failed to parse redis url", "error", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(redisOpts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		slog.Error("failed to ping redis", "error", err)
		os.Exit(1)
	}
	defer rdb.Close()

	// Build dependency graph.
	repo := repository.NewPostgresEventRepository(dbConn)
	svc := service.NewEventService(repo, rdb)
	handler := handlers.NewEventHandler(svc)
	proc := worker.NewProcessor(repo, rdb, cfg)

	// Setup Gin router.
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.StructuredLogger())
	r.Use(gin.Recovery())

	// Register routes.
	r.GET("/health", healthHandler)
	r.GET("/ready", readinessHandler(dbConn, rdb))
	r.POST("/events", handler.IngestEvent)
	r.GET("/events/:id", handler.GetEvent)
	r.GET("/events", handler.ListEvents)

	// Graceful shutdown via context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	// Channel to capture server errors.
	errCh := make(chan error, 1)

	// Start background worker.
	slog.Info("starting event processor worker")
	go proc.Run(ctx)
	go proc.RunReconciler(ctx, 30*time.Second, time.Minute, 100)

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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http server shutdown error", "error", err)
	}
	if err := proc.Wait(shutdownCtx); err != nil {
		slog.Error("worker shutdown error", "error", err)
	}
	if err := proc.WaitReconciler(shutdownCtx); err != nil {
		slog.Error("reconciler shutdown error", "error", err)
	}

	slog.Info("event processor stopped")
}

func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func readinessHandler(dbConn *sqlx.DB, rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 500*time.Millisecond)
		defer cancel()

		if err := dbConn.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "dependency": "postgres"})
			return
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "dependency": "redis"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
