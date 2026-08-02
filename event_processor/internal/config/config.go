package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the event processor service.
type Config struct {
	DatabaseURL            string
	RedisURL               string
	NotificationServiceURL string
	Port                   string
	MaxRetries             int
	RetryBaseDelay         time.Duration
}

// Load reads configuration from environment variables, applying defaults
// where applicable. Returns an error if any required variable is missing.
func Load() (*Config, error) {
	port := envOrDefault("PORT", "8080")
	if err := validatePort("PORT", port); err != nil {
		return nil, err
	}

	maxRetries, err := envOrDefaultInt("MAX_RETRIES", 3)
	if err != nil {
		return nil, err
	}
	if maxRetries < 0 {
		return nil, fmt.Errorf("MAX_RETRIES must be greater than or equal to 0")
	}

	retryBaseDelayMS, err := envOrDefaultInt("RETRY_BASE_DELAY_MS", 1000)
	if err != nil {
		return nil, err
	}
	if retryBaseDelayMS <= 0 {
		return nil, fmt.Errorf("RETRY_BASE_DELAY_MS must be greater than 0")
	}

	cfg := &Config{
		Port:           port,
		MaxRetries:     maxRetries,
		RetryBaseDelay: time.Duration(retryBaseDelayMS) * time.Millisecond,
	}

	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	cfg.RedisURL = os.Getenv("REDIS_URL")
	if cfg.RedisURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required")
	}

	cfg.NotificationServiceURL = os.Getenv("NOTIFICATION_SERVICE_URL")
	if cfg.NotificationServiceURL == "" {
		return nil, fmt.Errorf("NOTIFICATION_SERVICE_URL is required")
	}
	if err := validateHTTPURL("NOTIFICATION_SERVICE_URL", cfg.NotificationServiceURL); err != nil {
		return nil, err
	}

	return cfg, nil
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envOrDefaultInt(key string, defaultVal int) (int, error) {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return n, nil
	}
	return defaultVal, nil
}

func validatePort(key, value string) error {
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("%s must be a valid TCP port", key)
	}
	return nil
}

func validateHTTPURL(key, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be a valid http or https URL", key)
	}
	return nil
}
