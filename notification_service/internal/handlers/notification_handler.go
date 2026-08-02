package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/models"
	"github.com/iamjarryfeng/notify_system/notification_service/internal/service"
)

const maxListLimit = 100

// NotificationHandler holds dependencies for notification HTTP handlers.
type NotificationHandler struct {
	svc service.NotificationService
}

// NewNotificationHandler constructs a NotificationHandler.
func NewNotificationHandler(svc service.NotificationService) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// sendNotificationRequest is the expected JSON body for POST /notifications.
type sendNotificationRequest struct {
	EventID   string                 `json:"event_id"`
	EventType string                 `json:"event_type"`
	Payload   map[string]interface{} `json:"payload"`
}

// SendNotification handles POST /notifications
// Receives a processed event from the event_processor, selects the appropriate
// channel(s), dispatches the notification, and records the outcome.
func (h *NotificationHandler) SendNotification(c *gin.Context) {
	var req sendNotificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	// Validate required fields.
	if req.EventID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event_id is required"})
		return
	}
	if _, err := uuid.Parse(req.EventID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event_id must be a valid UUID"})
		return
	}
	if req.EventType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event_type is required"})
		return
	}
	if req.Payload == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload is required"})
		return
	}

	notifications, err := h.svc.SendNotification(c.Request.Context(), req.EventID, req.EventType, req.Payload)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidNotification):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrNoRoutes):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "no dispatch routes resolved"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		}
		return
	}

	c.JSON(http.StatusCreated, gin.H{"notifications": notifications})
}

// GetNotification handles GET /notifications/:id
func (h *NotificationHandler) GetNotification(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	notification, err := h.svc.GetNotification(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrNotificationNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "notification not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, notification)
}

// ListNotifications handles GET /notifications?event_id=&status=&limit=&offset=
func (h *NotificationHandler) ListNotifications(c *gin.Context) {
	eventID := c.Query("event_id")
	status := c.Query("status")
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}

	notifications, err := h.svc.ListNotifications(c.Request.Context(), eventID, status, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	if notifications == nil {
		notifications = make([]*models.Notification, 0)
	}

	c.JSON(http.StatusOK, gin.H{
		"notifications": notifications,
	})
}

func parsePagination(c *gin.Context) (int, int, bool) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
		return 0, 0, false
	}
	if limit > maxListLimit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be less than or equal to 100"})
		return 0, 0, false
	}

	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
		return 0, 0, false
	}

	return limit, offset, true
}
