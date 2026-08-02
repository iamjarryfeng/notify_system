# SOLUTION.md — Event-Driven Notification Platform

## Reviewer Summary

This submission implements the two-service event-driven notification platform end to end and verifies it with unit, integration, and Docker Compose acceptance testing.

### What Is Working

- `event_processor` accepts events via `POST /events`, persists them in PostgreSQL, enqueues them in Redis, and processes them asynchronously.
- The background worker dequeues events, forwards them to `notification_service`, retries transient downstream failures with exponential backoff, and updates event status to `processed` or `failed`.
- A lightweight reconciler periodically re-enqueues stale `pending` events, reducing the impact of the PostgreSQL-write/Redis-enqueue split without introducing a full outbox subsystem.
- `notification_service` resolves channel routes, validates payload recipients, dispatches notifications through channel abstractions, retries dispatcher failures with exponential backoff plus jitter, and persists notification outcomes in PostgreSQL.
- Notification records are created as `pending` before dispatch and then updated to `sent` or `failed`, so real provider side effects remain traceable even if a later status update fails.
- Notification dispatch is idempotent per `(event_id, channel)`, so repeated `POST /notifications` calls return existing records instead of dispatching duplicate email/webhook side effects.
- Both services expose working `GET` endpoints, `/health` liveness checks, and `/ready` dependency readiness checks.
- Request IDs are propagated across the async boundary by persisting them on events and forwarding them downstream in `X-Request-ID`.
- Duplicate event submission is handled through an optional client-supplied event UUID, returning `409 Conflict` on re-submission.

### Validation Performed

- Table-driven unit tests for core service logic and handler behavior in both services.
- Integration tests for `event_processor` covering HTTP ingest, PostgreSQL persistence, Redis queueing, worker processing, and downstream HTTP dispatch.
- Integration tests for `notification_service` covering real HTTP plus PostgreSQL using both `embedded-postgres` and `testcontainers-go`.
- Manual Docker Compose acceptance test covering stack startup, health checks, event ingestion, asynchronous processing, notification persistence, and duplicate-event rejection.

### Known Tradeoffs

- The services share one PostgreSQL database and separate tables rather than separate schemas.
- Email and webhook dispatchers remain stubs, but they are wrapped in retryable dispatcher abstractions for future replacement with real providers.
- Integration coverage exists for both services, but there is not yet one single unified cross-service test harness running both modules in-process.

## (a) Design Decisions

### 1. Service Layer Between Handlers and Repositories

I introduced a `service/` package in each microservice that sits between the HTTP handlers and repositories/dispatchers. This keeps handlers thin (HTTP parsing + status codes) and concentrates all business logic in the service layer where it can be tested without HTTP. The handlers map sentinel errors (`ErrEventNotFound`, etc.) to HTTP status codes.

### 2. QueueClient Abstraction for Redis

Instead of injecting `*redis.Client` directly into the service, I defined a small `QueueClient` interface with a single `Enqueue` method. A `redisQueueClient` adapter wraps the real Redis client. This makes the service testable without a running Redis instance and enables swapping the queue backend later.

One consequence of this separation is that enqueue failures can now be surfaced explicitly. The event is still persisted first, but if Redis cannot accept the event ID, the service returns a dedicated error and the handler maps it to `503 Service Unavailable` instead of returning a misleading `202 Accepted`.

### 3. Declarative Channel Routing

I built a `Router` struct in the notification service that maps event types to dispatch routes at startup. Each route specifies the channel name, a recipient extractor function, and a message builder function. The router supports a `"default"` fallback for unrecognised event types. This approach is:
- **Readable**: All routing rules are visible in one place (`router.go`).
- **Testable**: Each route can be tested independently.
- **Extensible**: Adding a new event type or channel requires only adding a new route entry.

### 4. Startup Migration Runner

Migrations are applied automatically at startup by reading `migrations/*.up.sql` files in sorted order. All DDL uses `IF NOT EXISTS`, making the runner idempotent — safe for re-runs. This avoids adding an external migration dependency while keeping the approach simple. In production, I would use a migration tool with a tracking table (e.g., `golang-migrate`).

### 5. Graceful Shutdown Without errgroup

I chose `signal.NotifyContext` + channel-based coordination rather than `golang.org/x/sync/errgroup` to avoid an additional dependency. The HTTP server now drains with a 1 minute timeout on shutdown, and the main process explicitly waits for the worker loop to stop before exiting. The worker stops dequeuing new jobs once the root context is cancelled, but finishes the in-flight processing cycle using a detached context so the current event is not abandoned mid-request.

### 6. Retry Strategy: Distinguishing 4xx from 5xx

The worker retries up to 3 times with exponential backoff (1s, 2s, 4s). Crucially, HTTP 4xx responses are treated as permanent failures — no retries. This prevents endless retries on bad payloads. 5xx and network errors are retried transiently. All retry events are logged at `WARN` level for observability.

### 7. Separate go.mod Per Service

Each service maintains its own `go.mod` with only the dependencies it needs. This keeps builds lean and avoids dependency conflicts between the two services.

### 8. Request ID Propagation Across the Async Boundary

Synchronous middleware-only request ID handling is not enough once processing continues in a background worker. To preserve traceability, the `event_processor` stores the request ID on the event record itself, reloads it in the worker, and forwards it to `notification_service` via `X-Request-ID`. This keeps log correlation intact across HTTP ingress, database persistence, queueing, and downstream notification dispatch.

### 9. Route Validation Before Dispatch

The notification service now validates that every resolved route has a usable recipient before sending anything. This avoids a partial-success case where the service would return `201 Created` but persist zero notification records because all recipients were empty. Requests missing mandatory payload fields for the resolved channels now return `400 Bad Request`.

### 10. Optional Client-Supplied Event IDs for Idempotency

`POST /events` now accepts an optional event `id`. When present, the service validates that it is a UUID and attempts to persist the event with that exact primary key. A duplicate insert is translated into a business-level duplicate-event error and returned as `409 Conflict`, which satisfies the idempotency expectation from the requirements without changing the asynchronous worker model.

### 11. Retrying Dispatcher Decorator in notification_service

The notification service now wraps each channel dispatcher in a retrying decorator that applies exponential backoff with jitter on transient send failures. This keeps retry policy close to the dispatch boundary rather than spreading it across handlers or repositories, avoids synchronized retry bursts, and preserves the existing `Dispatcher` interface so real SMTP or webhook implementations can be swapped in later without changing business logic.

### 12. Pending-First Notification Persistence

Notifications are persisted as `pending` before dispatcher calls are made, then updated to `sent` or `failed` after each dispatch attempt finishes. This gives the system a durable record before external side effects happen, which is safer for future real SMTP/webhook implementations than dispatching first and recording later.

### 13. Notification Idempotency

The notification table has a unique `(event_id, channel)` constraint. Inserts use an idempotent upsert path: duplicate requests return the existing notification row and skip duplicate dispatch. This protects the downstream service from repeated HTTP retries where the first response was lost after the notification was already recorded.

### 14. Startup Configuration Validation

Both services validate startup configuration instead of silently falling back on invalid environment values. Invalid ports, malformed retry counts, non-positive retry delays, and invalid downstream HTTP URLs fail fast with clear errors.

### 15. Bounded Pagination

List endpoints validate `limit` and `offset` instead of silently swallowing parse errors. `limit` is capped at 100 to avoid unbounded list queries turning into a denial-of-service vector.

### 16. Stale Pending Event Reconciler

`event_processor` still writes PostgreSQL and Redis separately, but it now has a recovery path for events that were persisted and not successfully queued. A background reconciler scans stale `pending` events and re-enqueues them. The worker's terminal-state guard makes duplicate queue entries safe.

### 17. Liveness vs Readiness

`/health` remains a lightweight liveness endpoint that only indicates the process is serving HTTP. `/ready` checks dependencies using existing clients and short timeouts: `event_processor` checks PostgreSQL and Redis, while `notification_service` checks PostgreSQL. This separates restart decisions from traffic-routing decisions.

---

## (b) What I Would Do Differently With More Time

1. **Production Migration Tooling**: The services now track applied migrations in per-service `schema_migrations_<service>` tables. With more time, I would replace the lightweight runner with a dedicated migration tool such as `golang-migrate` for down migrations, checksums, and operational tooling.

2. **Broader End-to-End Tests**: I added real HTTP + PostgreSQL integration tests for `notification_service` using both `embedded-postgres` and `testcontainers-go`, plus a real HTTP + PostgreSQL + Redis-path integration test for `event_processor` using `embedded-postgres` and `miniredis`. With more time, I would combine these into a single cross-service end-to-end test that boots both services together and verifies the full path: POST /events → worker dequeues → POST /notifications → notification row persisted.

3. **Real Dispatcher Implementations**: The email and webhook dispatchers are still stubs. With more time, I would implement real SMTP delivery (using `net/smtp` or a library) and real webhook HTTP calls behind the existing retrying-dispatcher wrapper.

4. **Dead Letter Queue**: When an event exhausts all retries and is marked `failed`, it is lost. A dead-letter queue (a separate Redis list) would allow failed events to be inspected and replayed manually.

5. **Observability**: Add Prometheus metrics (event ingest rate, processing latency, retry count, queue depth) and OpenTelemetry tracing. Request IDs already propagate across the async boundary, but distributed traces would make cross-service debugging much stronger.

6. **Pagination Metadata on List Endpoints**: The list endpoints now validate and cap pagination inputs. With more time, I would also return `total`, `next_offset`, and `prev_offset` in list responses to support proper client-side pagination.

7. **Richer Configuration Model**: Startup validation now catches invalid values. With more time, I would add typed config docs, examples, and validation for database and Redis URLs before attempting connections.

8. **Full Cross-Service Acceptance Harness**: The current integration coverage proves the `event_processor` path and the `notification_service` path independently with real HTTP and PostgreSQL. With more time, I would add one test harness that starts both services together and verifies the full cross-service flow in a single test process.

---

## (c) Assumptions

1. **Shared PostgreSQL Instance**: The two services share a single PostgreSQL database. In production, they might use separate databases or at least separate schemas.

2. **Single Redis Instance**: The Redis list is assumed to be a single-node instance. No Redis Cluster or Sentinel configuration is implemented.

3. **No Authentication**: Both services run without authentication, as specified in the requirements. In production, service-to-service communication would use mutual TLS or API keys.

4. **Monotonic Event IDs**: The system assumes event IDs are unique UUIDs generated by the database. No custom ID generation or collision handling is implemented.

5. **Event Processor is Single-Instance**: The background worker uses `BLPop` from a single Redis list. In a multi-instance deployment, this naturally load-balances (each event goes to one worker), but there is no coordination or leader election.

6. **Notification Service is Stateless**: The notification service does not maintain any in-memory state. All state is in PostgreSQL, making it safe to run multiple instances behind a load balancer.

7. **Migration Directory Path**: The migration runner expects a `migrations/` directory relative to the working directory. This works for `go run ./main.go` from the service root and for the Docker build (where `WORKDIR /app` and migrations are copied). A production deployment would use an absolute path or embed migrations.

8. **No Webhook Signature Verification**: The webhook dispatcher is a stub. A real implementation would sign outbound webhook payloads (HMAC-SHA256) for the receiver to verify.

9. **Integration Test Runtime Availability**: The integration tests use both `embedded-postgres` and `testcontainers-go`. Local runs skip unavailable integration dependencies, but CI can set `RUN_INTEGRATION=1` or use `make test-ci` to make missing dependencies fail the build instead of silently passing.

---

## (d) Requirement Coverage

### Core Requirements

- **`go run ./main.go` for both services**: Implemented and verified during local development and Docker image builds.
- **`POST /events` returns `202` and persists pending events**: Implemented.
- **Background worker dequeues events and calls notification-service**: Implemented.
- **notification-service persists notification records**: Implemented.
- **GET endpoints return correct data and status codes**: Implemented.
- **Graceful shutdown**: Implemented for in-flight HTTP requests and the current worker processing cycle.
- **`docker compose up` starts the full stack**: Implemented and acceptance-tested.

### Should Implement

- **Input validation with meaningful `400` errors**: Implemented.
- **Notification dispatch failure retries with exponential backoff**: Implemented in `notification_service` via a retrying dispatcher wrapper.
- **Structured JSON logging**: Implemented with `log/slog`.
- **At least one table-driven unit test per service**: Implemented.

### Expected / Bonus Items

- **Request ID propagation across service boundaries**: Implemented, including persistence across the async worker boundary.
- **Idempotency (`409` on duplicate event submission)**: Implemented using an optional client-supplied event UUID.
- **Pagination with `limit` and `offset`**: Implemented with validation and a max limit of 100.
- **Integration tests with real Postgres**: Implemented twice for `notification_service` (`embedded-postgres` and `testcontainers-go`) and once for `event_processor` (`embedded-postgres` + `miniredis`).

### Partial / Not Fully Aligned

- **Separate database schemas per service**: Not implemented. The services share one database and use separate tables, which satisfies the functional requirement but not the stricter interpretation of separate schemas.
- **Single cross-service acceptance harness**: Not implemented as one unified test process. The two services are verified independently in integration tests and together through Docker Compose acceptance testing.

---

## File Summary

### event_processor

| File | Purpose |
|------|---------|
| `main.go` | Full startup: config → DB → Redis → wire deps → routes → worker → graceful shutdown |
| `internal/config/config.go` | Environment variable loading with startup validation |
| `internal/config/config_test.go` | Table-driven tests for config validation |
| `internal/db/migrate.go` | Startup migration runner |
| `migrations/002_add_request_id_to_events.up.sql` | Adds persisted request IDs for async trace propagation |
| `internal/middleware/middleware.go` | RequestID + StructuredLogger middleware |
| `internal/service/event_service.go` | Business logic: validation, persistence, enqueuing + QueueClient interface |
| `internal/repository/event_repository.go` | PostgreSQL implementation of EventRepository (4 CRUD methods) |
| `internal/handlers/event_handler.go` | HTTP handlers: IngestEvent, GetEvent, ListEvents |
| `internal/worker/processor.go` | Background worker: BLPop dequeue, graceful shutdown wait, retry with backoff, request ID forwarding |
| `internal/service/event_service_test.go` | Table-driven tests for validation, request ID persistence, and enqueue failure |
| `internal/handlers/event_handler_test.go` | HTTP layer tests for success, request ID forwarding, 400/404, and 503 enqueue failure |
| `internal/worker/processor_test.go` | Regression test for in-flight shutdown completion and downstream request ID propagation |
| `integration_test.go` | Real HTTP + PostgreSQL + Redis-path integration test for ingest, queue, worker, and downstream dispatch |

### notification_service

| File | Purpose |
|------|---------|
| `main.go` | Full startup: config → DB → wire deps → routes → graceful shutdown |
| `internal/config/config.go` | Environment variable loading with startup validation |
| `internal/config/config_test.go` | Table-driven tests for config validation |
| `internal/db/migrate.go` | Startup migration runner |
| `migrations/002_allow_pending_notifications.up.sql` | Adds pending notification status for persistence-before-dispatch |
| `migrations/003_add_notification_idempotency.up.sql` | Adds unique `(event_id, channel)` idempotency constraint |
| `migrations/004_remove_unused_sms_channel.up.sql` | Removes the unused SMS channel from the notification channel check |
| `internal/middleware/middleware.go` | RequestID + StructuredLogger middleware |
| `internal/repository/notification_repository.go` | PostgreSQL implementation (4 CRUD methods) |
| `internal/service/router.go` | Declarative channel routing (4 event types + default) |
| `internal/service/notification_service.go` | Business logic: validate routes → idempotently persist pending → dispatch → update outcome |
| `internal/channels/dispatcher.go` | EmailDispatcher + WebhookDispatcher (stub implementations) |
| `internal/channels/retrying_dispatcher.go` | Exponential-backoff retry wrapper with jitter around dispatchers |
| `internal/handlers/notification_handler.go` | HTTP handlers: SendNotification, GetNotification, ListNotifications |
| `internal/channels/retrying_dispatcher_test.go` | Unit tests for dispatcher retry behavior |
| `internal/service/router_test.go` | Route resolution tests (5 event types + message verification) |
| `internal/service/notification_service_test.go` | Business logic tests (single/multi-channel, missing recipient, retry behavior, not found) |
| `internal/handlers/notification_handler_test.go` | HTTP layer tests (success, validation 400s, and 404) |
| `integration_test.go` | Real HTTP + PostgreSQL integration test using embedded-postgres |
| `integration_testcontainers_test.go` | Real HTTP + PostgreSQL integration test using testcontainers-go |
