package service

import (
	"fmt"
	"log/slog"

	"github.com/jarryfeng/notify_system/notification_service/internal/channels"
)

// Route describes a single notification dispatch target for a given event type.
type Route struct {
	Channel   string
	Recipient func(payload map[string]interface{}) string
	Message   func(payload map[string]interface{}, eventType string) string
}

// Router resolves event types to the appropriate dispatch routes.
type Router struct {
	routes map[string][]Route
}

// NewRouter builds a router with all routing rules registered.
func NewRouter(dispatchers map[string]channels.Dispatcher) *Router {
	r := &Router{
		routes: make(map[string][]Route),
	}

	// user.registered → email
	r.routes["user.registered"] = []Route{
		{
			Channel:   "email",
			Recipient: func(payload map[string]interface{}) string { return getString(payload, "email") },
			Message: func(payload map[string]interface{}, eventType string) string {
				return "Welcome! Your account has been registered."
			},
		},
	}

	// order.completed → email + webhook
	r.routes["order.completed"] = []Route{
		{
			Channel:   "email",
			Recipient: func(payload map[string]interface{}) string { return getString(payload, "email") },
			Message: func(payload map[string]interface{}, eventType string) string {
				return "Your order has been completed."
			},
		},
		{
			Channel:   "webhook",
			Recipient: func(payload map[string]interface{}) string { return getString(payload, "webhook_url") },
			Message: func(payload map[string]interface{}, eventType string) string {
				email := getString(payload, "email")
				return fmt.Sprintf("Order completed for customer: %s", email)
			},
		},
	}

	// payment.failed → email (urgent)
	r.routes["payment.failed"] = []Route{
		{
			Channel:   "email",
			Recipient: func(payload map[string]interface{}) string { return getString(payload, "email") },
			Message: func(payload map[string]interface{}, eventType string) string {
				return "[URGENT] Your payment has failed. Please update your payment method."
			},
		},
	}

	// default → webhook (fallback for any unrecognized event type)
	r.routes["default"] = []Route{
		{
			Channel:   "webhook",
			Recipient: func(payload map[string]interface{}) string { return getString(payload, "webhook_url") },
			Message: func(payload map[string]interface{}, eventType string) string {
				return fmt.Sprintf("Event received: %s", eventType)
			},
		},
	}

	// Validate that every referenced channel has a registered dispatcher.
	for eventType, routes := range r.routes {
		for _, route := range routes {
			if _, ok := dispatchers[route.Channel]; !ok {
				slog.Warn("no dispatcher registered for channel",
					"event_type", eventType,
					"channel", route.Channel)
			}
		}
	}

	return r
}

// Resolve returns the routes for a given event type, falling back to "default".
func (r *Router) Resolve(eventType string, payload map[string]interface{}) []Route {
	if routes, ok := r.routes[eventType]; ok {
		return routes
	}
	return r.routes["default"]
}

// getString safely extracts a string value from a payload map.
func getString(payload map[string]interface{}, key string) string {
	if v, ok := payload[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
