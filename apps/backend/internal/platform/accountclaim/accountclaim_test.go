package accountclaim

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNeedsReset(t *testing.T) {
	if !NeedsReset(nil) {
		t.Fatal("an unconfirmed address needs a reset")
	}
	now := time.Now()
	if NeedsReset(&now) {
		t.Fatal("a confirmed address needs no reset")
	}
}

func TestClaim_ConfirmedAddressDoesNothing(t *testing.T) {
	now := time.Now()
	// A nil transaction proves nothing is read or written.
	claimed, jobID, err := Claim(context.Background(), nil, &now, SetupLetter{UserID: uuid.New(), Email: "a@example.test"})
	if err != nil || claimed || jobID != "" {
		t.Fatalf("Claim = %v %q %v, want false, empty, nil", claimed, jobID, err)
	}
}

func TestQueueSetupLetter_RefusesAnIncompleteLetter(t *testing.T) {
	ctx := context.Background()
	if _, err := QueueSetupLetter(ctx, nil, SetupLetter{Email: "a@example.test", ExpiresAt: time.Now()}); err == nil {
		t.Fatal("a letter without a user was queued")
	}
	if _, err := QueueSetupLetter(ctx, nil, SetupLetter{UserID: uuid.New(), ExpiresAt: time.Now()}); err == nil {
		t.Fatal("a letter without an address was queued")
	}
	if _, err := QueueSetupLetter(ctx, nil, SetupLetter{UserID: uuid.New(), Email: "a@example.test"}); err == nil {
		t.Fatal("a letter without an expiry was queued")
	}
}
