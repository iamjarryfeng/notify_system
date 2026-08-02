package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/iamjarryfeng/notify_system/notification_service/internal/channels"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/middleware"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/models"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/repository"
)

// Sentinel errors for notification business logic.
var (
	ErrNoRoutes             = errors.New("no dispatch routes resolved for event type")
	ErrInvalidNotification  = errors.New("payload is missing a required recipient for one or more notification channels")
	ErrNotificationNotFound = errors.New("notification not found")
)

// NotificationService defines the business logic contract for notifications.
type NotificationService interface {
	SendNotification(ctx context.Context, eventID, eventType string, payload map[string]interface{}) ([]*models.Notification, error)
	GetNotification(ctx context.Context, id string) (*models.Notification, error)
	ListNotifications(ctx context.Context, eventID, status string, limit, offset int) ([]*models.Notification, error)
}

type notificationService struct {
	repo        repository.NotificationRepository
	router      *Router
	dispatchers map[string]channels.Dispatcher
}

type resolvedDispatch struct {
	dispatcher channels.Dispatcher
	channel    string
	recipient  string
	message    string
}

// NewNotificationService constructs a NotificationService.
func NewNotificationService(
	repo repository.NotificationRepository,
	router *Router,
	dispatchers map[string]channels.Dispatcher,
) NotificationService {
	return &notificationService{
		repo:        repo,
		router:      router,
		dispatchers: dispatchers,
	}
}

// SendNotification resolves routes, dispatches notifications, and records outcomes.
func (s *notificationService) SendNotification(
	ctx context.Context,
	eventID, eventType string,
	payload map[string]interface{},
) ([]*models.Notification, error) {
	requestID := middleware.RequestIDFromContext(ctx)
	logAttrs := []any{"request_id", requestID, "event_id", eventID, "event_type", eventType}
	routes := s.router.Resolve(eventType, payload)
	if len(routes) == 0 {
		return nil, ErrNoRoutes
	}

	dispatches := make([]resolvedDispatch, 0, len(routes))
	for _, route := range routes {
		recipient := route.Recipient(payload)
		if recipient == "" {
			slog.Warn("rejecting notification request: empty recipient",
				append(logAttrs, "channel", route.Channel)...)
			return nil, ErrInvalidNotification
		}

		dispatcher, ok := s.dispatchers[route.Channel]
		if !ok {
			return nil, fmt.Errorf("no dispatcher for channel %q", route.Channel)
		}

		dispatches = append(dispatches, resolvedDispatch{
			dispatcher: dispatcher,
			channel:    route.Channel,
			recipient:  recipient,
			message:    route.Message(payload, eventType),
		})
	}

	notifications := make([]*models.Notification, 0, len(dispatches))
	for _, dispatch := range dispatches {
		notifications = append(notifications, &models.Notification{
			EventID:   eventID,
			Channel:   dispatch.channel,
			Recipient: dispatch.recipient,
			Message:   dispatch.message,
			Status:    "pending",
		})
	}

	if err := s.repo.SaveAll(ctx, notifications); err != nil {
		slog.Error("failed to save pending notification records",
			append(logAttrs, "error", err)...)
		return nil, fmt.Errorf("save pending notifications: %w", err)
	}

	for i, dispatch := range dispatches {
		notification := notifications[i]
		if !notification.Inserted {
			slog.Info("notification already recorded, skipping duplicate dispatch",
				append(logAttrs, "notification_id", notification.ID, "channel", notification.Channel, "status", notification.Status)...)
			continue
		}

		status := "sent"
		var sentAt *time.Time

		if err := dispatch.dispatcher.Send(ctx, dispatch.recipient, dispatch.message); err != nil {
			slog.Error("dispatch failed",
				append(logAttrs, "channel", dispatch.channel, "error", err)...)
			status = "failed"
		} else {
			now := time.Now()
			sentAt = &now
		}

		if err := s.repo.UpdateStatus(ctx, notification.ID, status, sentAt); err != nil {
			slog.Error("failed to update notification status",
				append(logAttrs, "notification_id", notification.ID, "status", status, "error", err)...)
			return nil, fmt.Errorf("update notification status: %w", err)
		}
		notification.Status = status
		notification.SentAt = sentAt
	}

	return notifications, nil
}

// GetNotification retrieves a single notification by ID.
func (s *notificationService) GetNotification(ctx context.Context, id string) (*models.Notification, error) {
	notification, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get notification: %w", err)
	}
	if notification == nil {
		return nil, ErrNotificationNotFound
	}
	return notification, nil
}

// ListNotifications returns notifications with optional filters and pagination.
func (s *notificationService) ListNotifications(ctx context.Context, eventID, status string, limit, offset int) ([]*models.Notification, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.List(ctx, eventID, status, limit, offset)
}
