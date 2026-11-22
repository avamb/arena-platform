package accountclaim

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNeedsReset_ConfirmedAddressReadsNothing(t *testing.T) {
	now := time.Now()
	// A nil transaction proves the database is not read.
	needs, err := NeedsReset(context.Background(), nil, uuid.New(), &now)
	if err != nil || needs {
		t.Fatalf("NeedsReset = %v %v, want false, nil for a confirmed address", needs, err)
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
