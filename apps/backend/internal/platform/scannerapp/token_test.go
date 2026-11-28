package scannerapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

func TestNewToken_ShapeAndHash(t *testing.T) {
	raw, hash, err := scannerapp.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(raw, scannerapp.TokenWirePrefix) {
		t.Errorf("raw %q lacks prefix %q", raw, scannerapp.TokenWirePrefix)
	}
	if len(raw) != len(scannerapp.TokenWirePrefix)+43 {
		t.Errorf("raw length = %d, want %d", len(raw), len(scannerapp.TokenWirePrefix)+43)
	}
	if !scannerapp.LooksLikeDeviceToken(raw) {
		t.Errorf("LooksLikeDeviceToken(%q) = false", raw)
	}
	if hash != scannerapp.HashToken(raw) {
		t.Error("hash differs from HashToken(raw)")
	}
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		t.Errorf("hash %q is not lowercase SHA-256 hex", hash)
	}
	raw2, _, err := scannerapp.NewToken()
	if err != nil {
		t.Fatalf("NewToken #2: %v", err)
	}
	if raw2 == raw {
		t.Error("two tokens are equal")
	}
}

func TestLooksLikeDeviceToken_Rejects(t *testing.T) {
	good, _, err := scannerapp.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"empty":       "",
		"prefix-only": scannerapp.TokenWirePrefix,
		"api-key":     "ak_" + good[len(scannerapp.TokenWirePrefix):],
		"short":       good[:len(good)-1],
		"long":        good + "A",
		"bad-char":    good[:len(good)-1] + "=",
		"space":       good[:len(good)-1] + " ",
	} {
		if scannerapp.LooksLikeDeviceToken(raw) {
			t.Errorf("%s: LooksLikeDeviceToken(%q) = true", name, raw)
		}
	}
}

func TestAuthenticate_MalformedNeverReadsDatabase(t *testing.T) {
	// A nil DBTX would panic if Authenticate touched the database.
	_, err := scannerapp.Authenticate(context.Background(), nil, "Bearer nonsense", time.Now())
	if !errors.Is(err, scannerapp.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestAccessTokenTTL(t *testing.T) {
	if scannerapp.AccessTokenTTL != time.Hour {
		t.Errorf("AccessTokenTTL = %v, want 1h", scannerapp.AccessTokenTTL)
	}
}
