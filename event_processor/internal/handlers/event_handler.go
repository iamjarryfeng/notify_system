package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jarryfeng/notify_system/event_processor/internal/models"
	"github.com/jarryfeng/notify_system/event_processor/internal/service"
)

const maxListLimit = 100

// EventHandler holds dependencies for event HTTP handlers.
type EventHandler struct {
	svc service.EventService
}

// NewEventHandler constructs an EventHandler.
func NewEventHandler(svc service.EventService) *EventHandler {
	return &EventHandler{svc: svc}
}

// ingestEventRequest is the expected JSON body for POST /events.
type ingestEventRequest struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// IngestEvent handles POST /events
// It validates the incoming event, persists it, and enqueues it for async processing.
func (h *EventHandler) IngestEvent(c *gin.Context) {
	var req ingestEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	event, err := h.svc.IngestEvent(c.Request.Context(), c.GetString("request_id"), req.ID, req.Type, req.Payload)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidEventID):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrInvalidEventType):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrInvalidPayload):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrDuplicateEvent):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrEnqueueFailed):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		}
		return
	}

	c.JSON(http.StatusAccepted, event)
}

// GetEvent handles GET /events/:id
func (h *EventHandler) GetEvent(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	event, err := h.svc.GetEvent(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrEventNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "event not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, event)
}

// ListEvents handles GET /events?status=&limit=&offset=
func (h *EventHandler) ListEvents(c *gin.Context) {
	status := c.Query("status")
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}

	events, err := h.svc.ListEvents(c.Request.Context(), status, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	if events == nil {
		events = make([]*models.Event, 0)
	}

	c.JSON(http.StatusOK, gin.H{
		"events": events,
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
