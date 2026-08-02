package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name: "valid config",
			env: map[string]string{
				"DATABASE_URL":             "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"REDIS_URL":                "redis://localhost:6379/0",
				"NOTIFICATION_SERVICE_URL": "http://localhost:8081",
				"MAX_RETRIES":              "2",
				"RETRY_BASE_DELAY_MS":      "250",
			},
		},
		{
			name: "invalid max retries",
			env: map[string]string{
				"DATABASE_URL":             "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"REDIS_URL":                "redis://localhost:6379/0",
				"NOTIFICATION_SERVICE_URL": "http://localhost:8081",
				"MAX_RETRIES":              "nope",
			},
			wantErr: "MAX_RETRIES must be an integer",
		},
		{
			name: "invalid notification URL",
			env: map[string]string{
				"DATABASE_URL":             "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"REDIS_URL":                "redis://localhost:6379/0",
				"NOTIFICATION_SERVICE_URL": "localhost:8081",
			},
			wantErr: "NOTIFICATION_SERVICE_URL must be a valid http or https URL",
		},
		{
			name: "non-positive retry delay",
			env: map[string]string{
				"DATABASE_URL":             "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"REDIS_URL":                "redis://localhost:6379/0",
				"NOTIFICATION_SERVICE_URL": "http://localhost:8081",
				"RETRY_BASE_DELAY_MS":      "0",
			},
			wantErr: "RETRY_BASE_DELAY_MS must be greater than 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			cfg, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.MaxRetries != 2 {
				t.Fatalf("expected max retries 2, got %d", cfg.MaxRetries)
			}
			if cfg.RetryBaseDelay != 250*time.Millisecond {
				t.Fatalf("expected retry base delay 250ms, got %s", cfg.RetryBaseDelay)
			}
		})
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DATABASE_URL",
		"REDIS_URL",
		"NOTIFICATION_SERVICE_URL",
		"PORT",
		"MAX_RETRIES",
		"RETRY_BASE_DELAY_MS",
	} {
		t.Setenv(key, "")
	}
}
