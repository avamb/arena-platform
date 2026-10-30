package config

import (
	"strings"
	"testing"
)

func TestLoad_MediaVariantWorkers(t *testing.T) {
	cases := []struct {
		name, value string
		want        int
		wantErr     bool
	}{
		{"default", "", 1, false},
		{"two", "2", 2, false},
		{"max", "8", 8, false},
		{"zero", "0", 0, true},
		{"too_many", "9", 0, true},
		{"negative", "-1", 0, true},
		{"not_a_number", "many", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			es := newEnvSetter(t)
			es.set("APP_ENV", "development")
			es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
			es.set("JWT_SIGNING_SECRET", "dev-secret")
			es.set("ENABLE_DEV_AUTH", "true")
			if c.value != "" {
				es.set("MEDIA_VARIANT_WORKERS", c.value)
			}
			cfg, err := Load()
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), "MEDIA_VARIANT_WORKERS") {
					t.Fatalf("want an error naming MEDIA_VARIANT_WORKERS, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MediaVariantWorkers != c.want {
				t.Fatalf("MediaVariantWorkers = %d, want %d", cfg.MediaVariantWorkers, c.want)
			}
		})
	}
}
