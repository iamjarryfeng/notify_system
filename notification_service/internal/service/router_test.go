package service

import (
	"testing"

	"github.com/iamjarryfeng/notify_system/notification_service/internal/channels"
)

func TestRouterResolve(t *testing.T) {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)

	tests := []struct {
		name           string
		eventType      string
		payload        map[string]interface{}
		wantChannels   []string
		wantRecipients []string
	}{
		{
			name:           "user.registered routes to email",
			eventType:      "user.registered",
			payload:        map[string]interface{}{"email": "test@example.com", "user_id": "abc"},
			wantChannels:   []string{"email"},
			wantRecipients: []string{"test@example.com"},
		},
		{
			name:           "order.completed routes to email and webhook",
			eventType:      "order.completed",
			payload:        map[string]interface{}{"email": "test@example.com", "webhook_url": "https://hook.example.com"},
			wantChannels:   []string{"email", "webhook"},
			wantRecipients: []string{"test@example.com", "https://hook.example.com"},
		},
		{
			name:           "payment.failed routes to email with urgent message",
			eventType:      "payment.failed",
			payload:        map[string]interface{}{"email": "test@example.com"},
			wantChannels:   []string{"email"},
			wantRecipients: []string{"test@example.com"},
		},
		{
			name:           "unknown event type falls back to webhook",
			eventType:      "unknown.event",
			payload:        map[string]interface{}{"webhook_url": "https://fallback-hook.example.com"},
			wantChannels:   []string{"webhook"},
			wantRecipients: []string{"https://fallback-hook.example.com"},
		},
		{
			name:           "missing email in user.registered returns empty recipient",
			eventType:      "user.registered",
			payload:        map[string]interface{}{"user_id": "no-email"},
			wantChannels:   []string{"email"},
			wantRecipients: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := router.Resolve(tt.eventType, tt.payload)

			if len(routes) != len(tt.wantChannels) {
				t.Fatalf("expected %d routes, got %d", len(tt.wantChannels), len(routes))
			}

			for i, route := range routes {
				if route.Channel != tt.wantChannels[i] {
					t.Errorf("route %d: expected channel %q, got %q", i, tt.wantChannels[i], route.Channel)
				}
				recipient := route.Recipient(tt.payload)
				if recipient != tt.wantRecipients[i] {
					t.Errorf("route %d: expected recipient %q, got %q", i, tt.wantRecipients[i], recipient)
				}
			}
		})
	}
}

func TestRouterPaymentFailedMessage(t *testing.T) {
	dispatchers := map[string]channels.Dispatcher{
		"email":   &channels.EmailDispatcher{},
		"webhook": &channels.WebhookDispatcher{},
	}
	router := NewRouter(dispatchers)

	routes := router.Resolve("payment.failed", map[string]interface{}{"email": "test@example.com"})
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}

	msg := routes[0].Message(map[string]interface{}{"email": "test@example.com"}, "payment.failed")
	if msg == "" {
		t.Error("expected non-empty message for payment.failed")
	}
	if msg[:7] != "[URGENT" {
		t.Error("expected [URGENT] prefix in payment.failed message")
	}
}
