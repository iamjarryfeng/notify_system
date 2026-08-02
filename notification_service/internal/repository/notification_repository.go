package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/models"
)

// NotificationRepository defines the persistence contract for notifications.
type NotificationRepository interface {
	Save(ctx context.Context, n *models.Notification) error
	SaveAll(ctx context.Context, notifications []*models.Notification) error
	FindByID(ctx context.Context, id string) (*models.Notification, error)
	List(ctx context.Context, eventID, status string, limit, offset int) ([]*models.Notification, error)
	UpdateStatus(ctx context.Context, id, status string, sentAt *time.Time) error
}

// postgresNotificationRepository is the PostgreSQL implementation.
type postgresNotificationRepository struct {
	db *sqlx.DB
}

// NewPostgresNotificationRepository constructs a repository backed by PostgreSQL.
func NewPostgresNotificationRepository(db *sqlx.DB) NotificationRepository {
	return &postgresNotificationRepository{db: db}
}

func (r *postgresNotificationRepository) Save(ctx context.Context, n *models.Notification) error {
	return saveNotification(ctx, r.db, n)
}

func (r *postgresNotificationRepository) SaveAll(ctx context.Context, notifications []*models.Notification) error {
	if len(notifications) == 0 {
		return nil
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification transaction: %w", err)
	}
	defer tx.Rollback()

	for _, notification := range notifications {
		if err := saveNotification(ctx, tx, notification); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit notification transaction: %w", err)
	}
	return nil
}

type queryRowExecutor interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

func saveNotification(ctx context.Context, executor queryRowExecutor, n *models.Notification) error {
	query := `
		INSERT INTO notifications (event_id, channel, recipient, message, status, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (event_id, channel)
		DO UPDATE SET event_id = notifications.event_id
		RETURNING id, status, sent_at, created_at, (xmax = 0) AS inserted`

	if n.Status == "" {
		n.Status = "pending"
	}

	err := executor.QueryRowContext(ctx, query,
		n.EventID, n.Channel, n.Recipient, n.Message, n.Status, n.SentAt,
	).Scan(&n.ID, &n.Status, &n.SentAt, &n.CreatedAt, &n.Inserted)
	if err != nil {
		return fmt.Errorf("save notification: %w", err)
	}

	return nil
}

func (r *postgresNotificationRepository) FindByID(ctx context.Context, id string) (*models.Notification, error) {
	query := `SELECT id, event_id, channel, recipient, message, status, sent_at, created_at
		FROM notifications WHERE id = $1`

	var n models.Notification
	err := r.db.QueryRowContext(ctx, query, id).
		Scan(&n.ID, &n.EventID, &n.Channel, &n.Recipient, &n.Message, &n.Status, &n.SentAt, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find notification by id: %w", err)
	}
	return &n, nil
}

func (r *postgresNotificationRepository) List(ctx context.Context, eventID, status string, limit, offset int) ([]*models.Notification, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	query := `SELECT id, event_id, channel, recipient, message, status, sent_at, created_at
		FROM notifications
		WHERE ($1 = '' OR event_id::text = $1)
		AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC
		LIMIT $3 OFFSET $4`

	rows, err := r.db.QueryxContext(ctx, query, eventID, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	var notifications []*models.Notification
	for rows.Next() {
		var n models.Notification
		if err := rows.Scan(&n.ID, &n.EventID, &n.Channel, &n.Recipient, &n.Message, &n.Status, &n.SentAt, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		notifications = append(notifications, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return notifications, nil
}

func (r *postgresNotificationRepository) UpdateStatus(ctx context.Context, id, status string, sentAt *time.Time) error {
	query := `UPDATE notifications SET status = $1, sent_at = $2 WHERE id = $3`

	result, err := r.db.ExecContext(ctx, query, status, sentAt, id)
	if err != nil {
		return fmt.Errorf("update notification status: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("notification %s not found", id)
	}
	return nil
}
