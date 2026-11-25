package config

import "testing"

// STRIPE_ELEMENTS_ENABLED is the embedded checkout's deployment switch
// (spec 65 §3.5): on unless an operator turns it off.
func TestStripeElementsEnabled_DefaultsOnAndParses(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		want  bool
	}{
		{set: false, want: true},
		{value: "", set: true, want: true},
		{value: "false", set: true, want: false},
		{value: "true", set: true, want: true},
	} {
		es := newEnvSetter(t)
		es.set("APP_ENV", "development")
		es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
		es.set("ENABLE_DEV_AUTH", "false")
		if tc.set {
			es.set("STRIPE_ELEMENTS_ENABLED", tc.value)
		} else {
			es.unset("STRIPE_ELEMENTS_ENABLED")
		}
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() with %q: %v", tc.value, err)
		}
		if cfg.StripeElementsEnabled != tc.want {
			t.Errorf("STRIPE_ELEMENTS_ENABLED=%q (set=%v): StripeElementsEnabled = %v, want %v", tc.value, tc.set, cfg.StripeElementsEnabled, tc.want)
		}
	}
}
