# Event-Driven Notification Platform

An event-driven notification platform built with two Go microservices.

- `event_processor` ingests events over HTTP, persists them in PostgreSQL, queues them in Redis, and processes them asynchronously.
- `notification_service` receives processed events, resolves notification channels, dispatches email/webhook messages, and records outcomes in PostgreSQL.

## Highlights

- **Reliable async processing**: HTTP ingestion is decoupled from delivery with PostgreSQL persistence, a Redis queue, and a background worker, so the API stays fast and failures do not silently lose events.
- **End-to-end idempotency**: duplicate event submissions return `409 Conflict`, and repeated notification dispatch is deduplicated by `(event_id, channel)` to prevent duplicate emails or webhooks.
- **Failure recovery built in**: stale pending events are periodically re-enqueued, retries use exponential backoff with jitter, and partial channel failures remain traceable through per-notification status updates.
- **Observable by default**: request IDs propagate across HTTP, PostgreSQL, Redis, and downstream calls; structured JSON logs plus separate `/health` and `/ready` probes make the system easy to operate.
- **Clean Go architecture**: two independent microservices use handler/service/repository separation, declarative channel routing, and swappable dispatcher abstractions for real provider integrations.
- **Tested end to end**: table-driven unit tests, real PostgreSQL integration tests, testcontainers coverage, and a one-command Docker Compose stack validate the full event path.

## Repository Layout

```
.
├── docker-compose.yml          # Local dev stack (Postgres + Redis + both services)
├── event_processor/            # Ingest, queue, and process events
├── notification_service/       # Resolve routes and dispatch notifications
├── Makefile                    # Build, test, and compose helpers
└── SOLUTION.md                 # Design decisions, tradeoffs, and verification notes
```

## Quick Start

```bash
# Build and start the full stack
docker compose up --build
```

Or run the services directly:

```bash
# Start infrastructure
docker compose up postgres redis -d

# Run event_processor (terminal 1)
cd event_processor
DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
REDIS_URL="redis://localhost:6379/0" \
NOTIFICATION_SERVICE_URL="http://localhost:8081" \
go run ./main.go

# Run notification_service (terminal 2)
cd notification_service
DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
go run ./main.go
```

## API Summary

| Service | Endpoint | Purpose |
|---------|----------|---------|
| event_processor | `POST /events` | Create and enqueue an event |
| event_processor | `GET /events/:id` | Fetch an event by ID |
| event_processor | `GET /events` | List events with `limit` and `offset` |
| event_processor | `GET /health`, `GET /ready` | Liveness and readiness probes |
| notification_service | `POST /notifications` | Send or deduplicate a notification |
| notification_service | `GET /notifications/:id` | Fetch a notification by ID |
| notification_service | `GET /notifications` | List notifications with `limit` and `offset` |
| notification_service | `GET /health`, `GET /ready` | Liveness and readiness probes |

## Validation

```bash
make build
make vet
make test
make compose-up
```

Set `RUN_INTEGRATION=1` or use `make test-ci` when integration dependencies must be available and failures should not be skipped.

## Configuration

Both services read configuration from environment variables. Required variables include:

| Variable | Service | Example |
|----------|---------|---------|
| `DATABASE_URL` | Both | `postgres://notify:notify@localhost:5432/notify?sslmode=disable` |
| `REDIS_URL` | event_processor | `redis://localhost:6379/0` |
| `NOTIFICATION_SERVICE_URL` | event_processor | `http://localhost:8081` |
