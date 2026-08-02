# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Project Overview

This repository contains an event-driven notification platform with two independent Go microservices.

- `event_processor/` ingests events via HTTP, persists them to PostgreSQL, enqueues them to Redis, processes them asynchronously, and forwards them to the notification service.
- `notification_service/` receives processed events, dispatches notifications through email and webhook channels, and records delivery status in PostgreSQL.

The two services use separate `go.mod` files and are built as separate Docker images.

## Common Commands

### Infrastructure

```bash
# Start PostgreSQL and Redis
docker compose up postgres redis -d

# Start the full stack
docker compose up -d

# Stop everything
docker compose down
```

### Event Processor (port 8080)

```bash
cd event_processor

DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
REDIS_URL="redis://localhost:6379/0" \
NOTIFICATION_SERVICE_URL="http://localhost:8081" \
go run ./main.go
```

### Notification Service (port 8081)

```bash
cd notification_service

DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
go run ./main.go
```

### Build and Test

```bash
make build
make vet
make test-internal
make test
make test-ci
```

## Architecture

### Event Processor Data Flow

```
POST /events
    |
    v
EventHandler.IngestEvent()
    |
    +-- EventRepository.Save() -> PostgreSQL events table (status: pending)
    +-- Redis queue push (event ID)
    |
    v
Processor.Run() [background goroutine]
    |
    +-- Redis dequeue (event ID)
    +-- EventRepository.FindByID() -> load full event
    +-- HTTP POST to notification_service
    +-- EventRepository.UpdateStatus() -> pending | processed | failed
```

### Notification Service Data Flow

```
POST /notifications
    |
    v
NotificationHandler.SendNotification()
    |
    +-- Persist all resolved notifications as pending in one transaction
    +-- Resolve Dispatcher by channel (email | webhook)
    +-- Dispatch and update status to sent or failed
```

### Key Design Patterns

- Repository pattern: each service defines repository interfaces and PostgreSQL implementations using `sqlx`.
- Service layer: handlers remain thin and business logic stays in testable service packages.
- Channel/Dispatcher abstraction: `notification_service/internal/channels/dispatcher.go` defines a `Dispatcher` interface for polymorphic notification delivery.
- Worker/processor loop: `worker.Processor.Run(ctx)` polls Redis and processes events with graceful shutdown.
- Retrying dispatcher decorator: exponential backoff with jitter wraps email and webhook dispatchers.
- Idempotency: duplicate notification requests are skipped using a unique `(event_id, channel)` constraint.
- Reconciler: stale pending events are periodically re-enqueued to recover from PostgreSQL/Redis write gaps.

### Database Schema

`event_processor` owns an `events` table with UUID ID, event type, JSONB payload, status, timestamps, and persisted request ID.

`notification_service` owns a `notifications` table with UUID ID, event ID, channel, recipient, message, status, and timestamps.

### Environment Variables

| Variable | Service | Required | Example |
|----------|---------|----------|---------|
| `DATABASE_URL` | Both | Yes | `postgres://notify:notify@localhost:5432/notify?sslmode=disable` |
| `REDIS_URL` | event_processor | Yes | `redis://localhost:6379/0` |
| `NOTIFICATION_SERVICE_URL` | event_processor | Yes | `http://localhost:8081` |

## Tech Stack

- Go with `gin-gonic/gin` for HTTP routing
- PostgreSQL with `jmoiron/sqlx`
- Redis with `go-redis/redis/v8`
- Docker multi-stage builds

## Implementation Notes

- Migrations run automatically at startup from each service's `migrations/` directory.
- Both services expose `/health` for liveness and `/ready` for dependency readiness.
- Integration tests are skipped locally when dependencies are unavailable; use `RUN_INTEGRATION=1` to make missing dependencies fail.
- The email and webhook dispatchers are stubs intended to be replaced with real provider integrations.
