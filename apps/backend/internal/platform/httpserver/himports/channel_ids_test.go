package himports

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// resolveChannelIDs runs BEFORE the import transaction; without a database
// (queries == nil) it must still parse and refuse cleanly.
func TestResolveChannelIDs_WithoutDatabase(t *testing.T) {
	t.Parallel()
	h := New(nil, nil, nil, nil)
	orgID := uuid.New()

	got, err := h.resolveChannelIDs(context.Background(), orgID, bil24compat.ImportSessionRequest{})
	if err != nil || got != nil {
		t.Fatalf("empty channelIds: got %v, %v; want nil, nil", got, err)
	}

	_, err = h.resolveChannelIDs(context.Background(), orgID, bil24compat.ImportSessionRequest{
		ChannelIDs: []string{"not-a-uuid"},
	})
	var ie *importError
	if !errors.As(err, &ie) || ie.status != http.StatusUnprocessableEntity || ie.code != "import.invalid_channel" {
		t.Fatalf("malformed id: got %v; want 422 import.invalid_channel", err)
	}

	_, err = h.resolveChannelIDs(context.Background(), orgID, bil24compat.ImportSessionRequest{
		ChannelIDs: []string{uuid.NewString()},
	})
	if !errors.As(err, &ie) || ie.code != "import.invalid_channel" {
		t.Fatalf("well-formed id without a database: got %v; want 422 import.invalid_channel", err)
	}
}

func TestPublicationHelpers(t *testing.T) {
	t.Parallel()
	if firstPublication(nil) != nil {
		t.Fatal("firstPublication(nil) must be nil")
	}
	a := ImportPublication{ChannelID: uuid.New()}
	b := ImportPublication{ChannelID: uuid.New()}
	if got := firstPublication([]ImportPublication{a, b}); got == nil || got.ChannelID != a.ChannelID {
		t.Fatalf("firstPublication should return the first binding, got %v", got)
	}
	if got := nonNilPublications(nil); got == nil || len(got) != 0 {
		t.Fatalf("nonNilPublications(nil) must be an empty array, got %v", got)
	}
	if got := nonNilPublications([]ImportPublication{a}); len(got) != 1 {
		t.Fatalf("nonNilPublications must keep the list, got %v", got)
	}
}

// channelIds is part of the event-bundle wire shape and decodes from JSON.
func TestImportSessionRequest_ChannelIDsDecode(t *testing.T) {
	t.Parallel()
	var req bil24compat.ImportSessionRequest
	if err := json.Unmarshal([]byte(`{"source":"arena","externalRef":"tg:x:1","publish":true,"channelIds":["a","b"]}`), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(req.ChannelIDs) != 2 || req.ChannelIDs[0] != "a" || !req.Publish {
		t.Fatalf("channelIds not decoded: %+v", req)
	}
}
