package channels

import (
	"context"
	"log/slog"
)

// Dispatcher is the common interface every notification channel must implement.
type Dispatcher interface {
	// Send delivers a message to recipient and returns an error on failure.
	Send(ctx context.Context, recipient, message string) error
	// Name returns the channel identifier (e.g. "email", "webhook").
	Name() string
}

// EmailDispatcher is a stub for an SMTP / transactional email provider.
// In production, this would hold SMTP config (host, port, credentials, etc.).
type EmailDispatcher struct{}

func (e *EmailDispatcher) Name() string { return "email" }

func (e *EmailDispatcher) Send(ctx context.Context, recipient, message string) error {
	slog.Info("email sent", "channel", "email", "recipient", recipient, "message_length", len(message))
	return nil
}

// WebhookDispatcher delivers notifications via HTTP POST to an external URL.
// The recipient parameter is interpreted as the webhook URL.
// In production, this would hold an *http.Client and optional auth config.
type WebhookDispatcher struct{}

func (w *WebhookDispatcher) Name() string { return "webhook" }

func (w *WebhookDispatcher) Send(ctx context.Context, recipient, message string) error {
	slog.Info("webhook dispatched", "channel", "webhook", "url", recipient, "message_length", len(message))
	return nil
}
