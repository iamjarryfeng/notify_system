package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type requestIDContextKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// RequestID ensures every request has a unique identifier for traceability.
// If the client sends an X-Request-ID header, it is reused; otherwise a new UUID is generated.
// The request ID is set on the response header and stored in the Gin context.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		c.Header("X-Request-ID", requestID)
		c.Set("request_id", requestID)
		c.Request = c.Request.WithContext(WithRequestID(c.Request.Context(), requestID))
		c.Next()
	}
}

// StructuredLogger logs each request with method, path, status, latency, and request_id.
func StructuredLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		defer func() {
			latency := time.Since(start)
			requestID, _ := c.Get("request_id")
			slog.Info("request",
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"status", c.Writer.Status(),
				"latency_ms", latency.Milliseconds(),
				"request_id", requestID,
				"client_ip", c.ClientIP(),
			)
		}()
		c.Next()
	}
}
