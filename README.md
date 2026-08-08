# notify_system

An event-driven notification platform in Go that turns HTTP events into reliable email and webhook notifications. Two small microservices, PostgreSQL, and Redis handle ingestion, asynchronous delivery, retries, idempotency, and end-to-end traceability.

<p align="center">
  <a href="https://github.com/iamjarryfeng/notify_system">
    <img alt="GitHub stars" src="https://img.shields.io/github/stars/iamjarryfeng/notify_system">
  </a>
  <a href="https://github.com/iamjarryfeng/notify_system/fork">
    <img alt="GitHub forks" src="https://img.shields.io/github/forks/iamjarryfeng/notify_system">
  </a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white">
  <img alt="PostgreSQL" src="https://img.shields.io/badge/PostgreSQL-16-336791?logo=postgresql&logoColor=white">
  <img alt="Redis" src="https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white">
  <img alt="Docker Compose" src="https://img.shields.io/badge/Docker_Compose-ok-2496ED?logo=docker&logoColor=white">
</p>

> This is a production-oriented reference implementation. The email and webhook dispatchers are pluggable stubs, so wire them to real providers before production traffic.

## Why notify_system?

- **Fast ingestion without blocking delivery.** `POST /events` persists the event, enqueues it, and returns `202 Accepted`; a background worker handles delivery.
- **Durable by default.** Events are stored in PostgreSQL before they are queued. A reconciler periodically re-enqueues stale `pending` events to recover from queue gaps.
- **No duplicate side effects.** Optional client-supplied event UUIDs produce `409 Conflict` on duplicate submissions, and notifications are deduplicated by `(event_id, channel)`.
- **Safe retries.** Worker retries distinguish permanent 4xx responses from transient 5xx and network failures, using exponential backoff with jitter.
- **Easy to trace.** Request IDs persist across HTTP, PostgreSQL, Redis, the worker, and downstream calls.
- **Operable.** Structured JSON logs, `/health`, `/ready`, graceful shutdown, and a one-command Docker Compose stack are included.
- **Clean and testable.** Handler/service/repository separation, declarative routing, dispatcher interfaces, and integration tests make the codebase easy to extend.

## Services

- `event_processor` ingests events over HTTP, persists them in PostgreSQL, queues them in Redis, and processes them asynchronously.
- `notification_service` receives processed events, resolves notification channels, dispatches email/webhook messages, and records outcomes in PostgreSQL.

## Architecture

```mermaid
flowchart LR
    Client["Client"] -->|"POST /events"| Ingest["event_processor"]
    Ingest -->|"INSERT pending"| DB[("PostgreSQL")]
    Ingest -->|"LPUSH event_id"| Queue[("Redis queue")]
    Worker["Worker"] -->|"BLPOP event_id"| Queue
    Worker -->|"find event + request_id"| DB
    Worker -->|"POST /notifications"| Notify["notification_service"]
    Notify -->|"INSERT pending"| DB
    Notify --> Email["email dispatcher"]
    Notify --> Webhook["webhook dispatcher"]
    Notify -->|"UPDATE sent / failed"| DB
    Reconciler["Reconciler"] -.->|"re-enqueue stale pending"| Queue
```

### How It Works

1. A client sends an event to `event_processor`. The service validates it, persists it as `pending`, enqueues the event ID in Redis, and returns `202 Accepted`.
2. The background worker pops the event ID, reloads the full event from PostgreSQL, and calls `notification_service`.
3. `notification_service` resolves routes for the event type, persists pending notifications, dispatches through the selected channels, and records `sent` or `failed`.
4. The reconciler re-enqueues events that remain `pending` too long, protecting against the PostgreSQL/Redis write gap.

## Quick Start

Prerequisites: Docker and Docker Compose.

```bash
git clone https://github.com/iamjarryfeng/notify_system.git
cd notify_system
docker compose up --build
```

Verify both services are ready:

```bash
curl -s http://localhost:8080/ready
curl -s http://localhost:8081/ready
```

Create an event:

```bash
curl -s -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "id": "11111111-1111-4111-8111-111111111111",
    "type": "user.registered",
    "payload": {
      "email": "user@example.com"
    }
  }'
```

Watch it move from the event queue into a notification:

```bash
curl -s http://localhost:8080/events/11111111-1111-4111-8111-111111111111
curl -s "http://localhost:8081/notifications?event_id=11111111-1111-4111-8111-111111111111"
```

If you omit `id`, PostgreSQL generates a UUID for the event. Providing the same UUID again returns `409 Conflict`.

## Run Without Docker

Start PostgreSQL and Redis, then run the services in separate terminals:

```bash
docker compose up postgres redis -d

# Terminal 1: event_processor
cd event_processor
DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
REDIS_URL="redis://localhost:6379/0" \
NOTIFICATION_SERVICE_URL="http://localhost:8081" \
go run ./main.go

# Terminal 2: notification_service
cd notification_service
DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
go run ./main.go
```

## Default Routing

| Event type | Channels | Required payload keys |
|------------|----------|------------------------|
| `user.registered` | email | `email` |
| `order.completed` | email + webhook | `email`, `webhook_url` |
| `payment.failed` | email | `email` |
| any other event | webhook | `webhook_url` |

The default dispatchers log a successful send. To integrate real providers, implement `channels.Dispatcher` and register it in `notification_service/main.go`; the retry wrapper is already applied around each dispatcher.

## HTTP API

### event_processor

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/events` | Ingest and enqueue an event; returns `202 Accepted` |
| `GET` | `/events/:id` | Fetch an event by ID |
| `GET` | `/events` | List events with `status`, `limit`, and `offset` |
| `GET` | `/health` | Liveness probe |
| `GET` | `/ready` | Readiness probe for PostgreSQL and Redis |

### notification_service

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/notifications` | Send or deduplicate a notification |
| `GET` | `/notifications/:id` | Fetch a notification by ID |
| `GET` | `/notifications` | List notifications with `event_id`, `status`, `limit`, and `offset` |
| `GET` | `/health` | Liveness probe |
| `GET` | `/ready` | Readiness probe for PostgreSQL |

List endpoints cap `limit` at `100` to keep queries bounded.

## Configuration

Both services read configuration from environment variables.

| Variable | Service | Default | Purpose |
|----------|---------|---------|---------|
| `DATABASE_URL` | both | required | PostgreSQL connection string |
| `REDIS_URL` | event_processor | required | Redis connection string |
| `NOTIFICATION_SERVICE_URL` | event_processor | required | downstream notification service URL |
| `PORT` | both | `8080` / `8081` | HTTP listen port |
| `MAX_RETRIES` | event_processor | `3` | worker attempts for the notification service |
| `RETRY_BASE_DELAY_MS` | event_processor | `1000` | base backoff delay for worker retries |
| `DISPATCH_MAX_RETRIES` | notification_service | `3` | attempts per notification dispatcher |
| `DISPATCH_RETRY_BASE_DELAY_MS` | notification_service | `1000` | base backoff delay for dispatcher retries |

## Testing

```bash
make build
make vet
make test
make test-ci
make compose-up
```

The test suite includes table-driven unit tests, real PostgreSQL integration tests, embedded PostgreSQL, miniredis, and testcontainers coverage. Integration tests skip when their dependencies are unavailable; set `RUN_INTEGRATION=1` or use `make test-ci` to require them.

## Repository Layout

```
.
├── docker-compose.yml          # Local dev stack: PostgreSQL, Redis, and both services
├── event_processor/            # Ingest, queue, process, and forward events
├── notification_service/       # Resolve routes and dispatch notifications
├── Makefile                    # Build, test, and compose helpers
├── SOLUTION.md                 # Design decisions, tradeoffs, and verification notes
└── CLAUDE.md                   # Developer-oriented working notes
```

## Roadmap

- Real SMTP and HTTP webhook providers behind the existing `Dispatcher` interface
- Dead-letter queue and replay for permanently failed events
- Prometheus metrics and OpenTelemetry tracing
- Transactional outbox for stronger persistence guarantees
- API authentication and service-to-service auth
- Pagination metadata such as `total` and `next_offset`

Contributions are welcome for any of these items.

## Contributing

1. Fork the repository and create a feature branch.
2. Keep changes small and focused.
3. Add or update tests for behavior changes.
4. Run `make vet` and `make test`.
5. Open a pull request with a clear description.

## Further Reading

- `SOLUTION.md` documents design decisions, tradeoffs, and validation notes.
- `CLAUDE.md` contains developer-oriented guidance for working in this repository.
