package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the notification service.
type Config struct {
	DatabaseURL            string
	Port                   string
	DispatchMaxRetries     int
	DispatchRetryBaseDelay time.Duration
}

// Load reads configuration from environment variables, applying defaults
// where applicable. Returns an error if any required variable is missing.
func Load() (*Config, error) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	if err := validatePort("PORT", port); err != nil {
		return nil, err
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	dispatchMaxRetries, err := envOrDefaultInt("DISPATCH_MAX_RETRIES", 3)
	if err != nil {
		return nil, err
	}
	if dispatchMaxRetries < 0 {
		return nil, fmt.Errorf("DISPATCH_MAX_RETRIES must be greater than or equal to 0")
	}

	dispatchRetryBaseDelayMS, err := envOrDefaultInt("DISPATCH_RETRY_BASE_DELAY_MS", 1000)
	if err != nil {
		return nil, err
	}
	if dispatchRetryBaseDelayMS <= 0 {
		return nil, fmt.Errorf("DISPATCH_RETRY_BASE_DELAY_MS must be greater than 0")
	}

	return &Config{
		DatabaseURL:            databaseURL,
		Port:                   port,
		DispatchMaxRetries:     dispatchMaxRetries,
		DispatchRetryBaseDelay: time.Duration(dispatchRetryBaseDelayMS) * time.Millisecond,
	}, nil
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
