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
				"DATABASE_URL":                 "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"DISPATCH_MAX_RETRIES":         "4",
				"DISPATCH_RETRY_BASE_DELAY_MS": "125",
			},
		},
		{
			name:    "missing database URL",
			env:     map[string]string{},
			wantErr: "DATABASE_URL is required",
		},
		{
			name: "invalid port",
			env: map[string]string{
				"DATABASE_URL": "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"PORT":         "70000",
			},
			wantErr: "PORT must be a valid TCP port",
		},
		{
			name: "negative dispatch retries",
			env: map[string]string{
				"DATABASE_URL":         "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"DISPATCH_MAX_RETRIES": "-1",
			},
			wantErr: "DISPATCH_MAX_RETRIES must be greater than or equal to 0",
		},
		{
			name: "invalid retry delay",
			env: map[string]string{
				"DATABASE_URL":                 "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"DISPATCH_RETRY_BASE_DELAY_MS": "fast",
			},
			wantErr: "DISPATCH_RETRY_BASE_DELAY_MS must be an integer",
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
			if cfg.DispatchMaxRetries != 4 {
				t.Fatalf("expected dispatch max retries 4, got %d", cfg.DispatchMaxRetries)
			}
			if cfg.DispatchRetryBaseDelay != 125*time.Millisecond {
				t.Fatalf("expected dispatch retry base delay 125ms, got %s", cfg.DispatchRetryBaseDelay)
			}
		})
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DATABASE_URL",
		"PORT",
		"DISPATCH_MAX_RETRIES",
		"DISPATCH_RETRY_BASE_DELAY_MS",
	} {
		t.Setenv(key, "")
	}
}
