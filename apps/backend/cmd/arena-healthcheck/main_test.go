package main

import (
	"os"
	"testing"
)

func TestListenToURL(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{":8080", "http://localhost:8080"},
		{":9091", "http://localhost:9091"},
		{"0.0.0.0:8080", "http://localhost:8080"},
		{"127.0.0.1:8080", "http://localhost:8080"},
		{"0.0.0.0:9091", "http://localhost:9091"},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			got := listenToURL(tc.addr)
			if got != tc.want {
				t.Errorf("listenToURL(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

func TestResolveTarget(t *testing.T) {
	// Helper to set env vars and restore them after the test.
	setenv := func(t *testing.T, key, value string) {
		t.Helper()
		old, hadOld := os.LookupEnv(key)
		os.Setenv(key, value)
		t.Cleanup(func() {
			if hadOld {
				os.Setenv(key, old)
			} else {
				os.Unsetenv(key)
			}
		})
	}
	unsetenv := func(t *testing.T, key string) {
		t.Helper()
		old, hadOld := os.LookupEnv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			if hadOld {
				os.Setenv(key, old)
			}
		})
	}

	t.Run("HEALTH_ADDR explicit wins over everything", func(t *testing.T) {
		setenv(t, "HEALTH_ADDR", "http://localhost:1234")
		setenv(t, "APP_NAME", "arena-worker")
		setenv(t, "WORKER_METRICS_ADDR", ":9091")
		got := resolveTarget()
		if got != "http://localhost:1234" {
			t.Errorf("got %q, want http://localhost:1234", got)
		}
	})

	t.Run("worker role uses WORKER_METRICS_ADDR", func(t *testing.T) {
		unsetenv(t, "HEALTH_ADDR")
		setenv(t, "APP_NAME", "arena-worker")
		setenv(t, "WORKER_METRICS_ADDR", ":9091")
		got := resolveTarget()
		if got != "http://localhost:9091" {
			t.Errorf("got %q, want http://localhost:9091", got)
		}
	})

	t.Run("worker role uses default :9091 when WORKER_METRICS_ADDR not set", func(t *testing.T) {
		unsetenv(t, "HEALTH_ADDR")
		setenv(t, "APP_NAME", "arena-worker")
		unsetenv(t, "WORKER_METRICS_ADDR")
		got := resolveTarget()
		if got != "http://localhost:9091" {
			t.Errorf("got %q, want http://localhost:9091", got)
		}
	})

	t.Run("api role uses HTTP_LISTEN_ADDR", func(t *testing.T) {
		unsetenv(t, "HEALTH_ADDR")
		setenv(t, "APP_NAME", "arena-api")
		setenv(t, "HTTP_LISTEN_ADDR", ":8080")
		got := resolveTarget()
		if got != "http://localhost:8080" {
			t.Errorf("got %q, want http://localhost:8080", got)
		}
	})

	t.Run("api role uses default :8080 when HTTP_LISTEN_ADDR not set", func(t *testing.T) {
		unsetenv(t, "HEALTH_ADDR")
		setenv(t, "APP_NAME", "arena-api")
		unsetenv(t, "HTTP_LISTEN_ADDR")
		got := resolveTarget()
		if got != "http://localhost:8080" {
			t.Errorf("got %q, want http://localhost:8080", got)
		}
	})

	t.Run("no env defaults to :8080 (API)", func(t *testing.T) {
		unsetenv(t, "HEALTH_ADDR")
		unsetenv(t, "APP_NAME")
		unsetenv(t, "HTTP_LISTEN_ADDR")
		got := resolveTarget()
		if got != "http://localhost:8080" {
			t.Errorf("got %q, want http://localhost:8080", got)
		}
	})
}
