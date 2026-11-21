package trackingconsent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	yes, no := true, false
	past := "2026-11-06T11:00:00Z"
	future := "2026-11-07T11:00:00Z"
	bad := "yesterday"

	c, err := Normalize(Input{Granted: &yes, Version: " v3 ", At: &past}, now)
	if err != nil || !c.Granted || c.Version != "v3" || !c.At.Equal(time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("a valid consent: %+v, %v", c, err)
	}
	c, err = Normalize(Input{Granted: &no, Version: "v3", At: &future}, now)
	if err != nil || c.Granted || !c.At.Equal(now) {
		t.Fatalf("a time in the future is replaced by the server's: %+v, %v", c, err)
	}
	if _, err := Normalize(Input{Version: "v3"}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("granted is required: %v", err)
	}
	if _, err := Normalize(Input{Granted: &yes}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("version is required: %v", err)
	}
	if _, err := Normalize(Input{Granted: &yes, Version: "v", At: &bad}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unreadable time is refused: %v", err)
	}
	if _, err := Normalize(Input{Granted: &yes, Version: "v", Categories: json.RawMessage(`[1]`)}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("categories must be an object: %v", err)
	}
}

func TestStoredAllows(t *testing.T) {
	if (Stored{}).Allows() {
		t.Fatal("a consent never stated must not allow a conversion")
	}
	if (Stored{Known: true}).Allows() {
		t.Fatal("a refusal must not allow a conversion")
	}
	if !(Stored{Known: true, Granted: true}).Allows() {
		t.Fatal("an explicit yes allows it")
	}
}
