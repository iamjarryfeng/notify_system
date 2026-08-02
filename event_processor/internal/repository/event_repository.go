package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/iamjarryfeng/notify_system/event_processor/internal/models"
)

var ErrDuplicateEvent = errors.New("event already exists")

// EventRepository defines the persistence contract for events.
type EventRepository interface {
	Save(ctx context.Context, event *models.Event) error
	FindByID(ctx context.Context, id string) (*models.Event, error)
	List(ctx context.Context, status string, limit, offset int) ([]*models.Event, error)
	ListPendingOlderThan(ctx context.Context, olderThan time.Duration, limit int) ([]*models.Event, error)
	UpdateStatus(ctx context.Context, id, status string) error
}

// postgresEventRepository is the PostgreSQL implementation.
type postgresEventRepository struct {
	db *sqlx.DB
}

// NewPostgresEventRepository constructs a repository backed by PostgreSQL.
func NewPostgresEventRepository(db *sqlx.DB) EventRepository {
	return &postgresEventRepository{db: db}
}

func (r *postgresEventRepository) Save(ctx context.Context, event *models.Event) error {
	// Marshal payload to JSON string for the JSONB column.
	payloadJSON, err := marshalPayload(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	query := `
		INSERT INTO events (request_id, type, payload, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id, status, created_at, updated_at`
	args := []interface{}{event.RequestID, event.Type, payloadJSON}
	if event.ID != "" {
		query = `
			INSERT INTO events (id, request_id, type, payload, status)
			VALUES ($1, $2, $3, $4, 'pending')
			RETURNING id, status, created_at, updated_at`
		args = []interface{}{event.ID, event.RequestID, event.Type, payloadJSON}
	}

	err = r.db.QueryRowContext(ctx, query, args...).
		Scan(&event.ID, &event.Status, &event.CreatedAt, &event.UpdatedAt)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return ErrDuplicateEvent
		}
		return fmt.Errorf("save event: %w", err)
	}
	return nil
}

func (r *postgresEventRepository) FindByID(ctx context.Context, id string) (*models.Event, error) {
	query := `SELECT id, request_id, type, payload, status, created_at, updated_at FROM events WHERE id = $1`

	var event models.Event
	var payloadBytes []byte
	err := r.db.QueryRowContext(ctx, query, id).
		Scan(&event.ID, &event.RequestID, &event.Type, &payloadBytes, &event.Status, &event.CreatedAt, &event.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find event by id: %w", err)
	}

	event.Payload, err = unmarshalPayload(payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	return &event, nil
}

func (r *postgresEventRepository) List(ctx context.Context, status string, limit, offset int) ([]*models.Event, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	query := `SELECT id, request_id, type, payload, status, created_at, updated_at FROM events
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := r.db.QueryxContext(ctx, query, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []*models.Event
	for rows.Next() {
		var event models.Event
		var payloadBytes []byte
		if err := rows.Scan(&event.ID, &event.RequestID, &event.Type, &payloadBytes, &event.Status, &event.CreatedAt, &event.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.Payload, err = unmarshalPayload(payloadBytes)
		if err != nil {
			return nil, fmt.Errorf("unmarshal payload: %w", err)
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return events, nil
}

func (r *postgresEventRepository) ListPendingOlderThan(ctx context.Context, olderThan time.Duration, limit int) ([]*models.Event, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT id, request_id, type, payload, status, created_at, updated_at FROM events
		WHERE status = 'pending'
		AND updated_at < NOW() - ($1 * INTERVAL '1 millisecond')
		ORDER BY updated_at ASC
		LIMIT $2`

	rows, err := r.db.QueryxContext(ctx, query, olderThan.Milliseconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("list stale pending events: %w", err)
	}
	defer rows.Close()

	var events []*models.Event
	for rows.Next() {
		var event models.Event
		var payloadBytes []byte
		if err := rows.Scan(&event.ID, &event.RequestID, &event.Type, &payloadBytes, &event.Status, &event.CreatedAt, &event.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan stale pending event: %w", err)
		}
		event.Payload, err = unmarshalPayload(payloadBytes)
		if err != nil {
			return nil, fmt.Errorf("unmarshal payload: %w", err)
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return events, nil
}

func (r *postgresEventRepository) UpdateStatus(ctx context.Context, id, status string) error {
	query := `UPDATE events SET status = $1, updated_at = NOW() WHERE id = $2`
	result, err := r.db.ExecContext(ctx, query, status, id)
	if err != nil {
		return fmt.Errorf("update event status: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("event %s not found", id)
	}
	return nil
}

// marshalPayload converts a map to JSON bytes for storage in JSONB.
func marshalPayload(payload map[string]interface{}) ([]byte, error) {
	if payload == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(payload)
}

// unmarshalPayload converts JSON bytes from JSONB back to a map.
func unmarshalPayload(data []byte) (map[string]interface{}, error) {
	if len(data) == 0 {
		return make(map[string]interface{}), nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
