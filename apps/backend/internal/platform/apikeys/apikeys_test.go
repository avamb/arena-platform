package apikeys_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

// fakeStore is an in-memory Store used for unit tests that must not touch a
// database. It mirrors the persistence semantics postgres_store.go relies
// on: a UNIQUE constraint on key_prefix.
type fakeStore struct {
	byPrefix map[string]apikeys.APIKey
	byID     map[uuid.UUID]*apikeys.APIKey
	touched  map[uuid.UUID][]time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byPrefix: map[string]apikeys.APIKey{},
		byID:     map[uuid.UUID]*apikeys.APIKey{},
		touched:  map[uuid.UUID][]time.Time{},
	}
}

func (s *fakeStore) InsertAPIKey(_ context.Context, orgID uuid.UUID, channelID *uuid.UUID, name, keyPrefix, keyHash string, scopes []string, createdBy uuid.UUID, expiresAt *time.Time) (apikeys.APIKey, error) {
	if _, exists := s.byPrefix[keyPrefix]; exists {
		return apikeys.APIKey{}, errors.New("fakeStore: duplicate key_prefix")
	}
	k := apikeys.APIKey{
		ID:        uuid.New(),
		OrgID:     orgID,
		ChannelID: channelID,
		Name:      name,
		KeyPrefix: keyPrefix,
		KeyHash:   keyHash,
		Scopes:    scopes,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}
	s.byPrefix[keyPrefix] = k
	kk := k
	s.byID[k.ID] = &kk
	return k, nil
}

func (s *fakeStore) GetAPIKeyByPrefix(_ context.Context, keyPrefix string) (apikeys.APIKey, error) {
	k, ok := s.byPrefix[keyPrefix]
	if !ok {
		return apikeys.APIKey{}, apikeys.ErrNotFound
	}
	// Reflect any in-place mutation (revoke/touch) made via s.byID.
	if stored, ok := s.byID[k.ID]; ok {
		return *stored, nil
	}
	return k, nil
}

func (s *fakeStore) TouchLastUsed(_ context.Context, id uuid.UUID, at time.Time) error {
	s.touched[id] = append(s.touched[id], at)
	if k, ok := s.byID[id]; ok {
		t := at
		k.LastUsedAt = &t
	}
	return nil
}

func (s *fakeStore) revoke(prefix string) {
	k := s.byPrefix[prefix]
	if stored, ok := s.byID[k.ID]; ok {
		t := time.Now()
		stored.RevokedAt = &t
	}
}

func TestValidateScopes(t *testing.T) {
	cases := []struct {
		name    string
		scopes  []string
		wantErr error
	}{
		{"empty", nil, apikeys.ErrEmptyScopes},
		{"platform-prefix", []string{"platform.admin"}, apikeys.ErrForbiddenScope},
		{"admin-prefix", []string{"event.read", "admin.users"}, apikeys.ErrForbiddenScope},
		{"api-key-manage", []string{"api_key.manage"}, apikeys.ErrForbiddenScope},
		{"ok-set", []string{
			"event.create", "event.read", "event.update", "event.publish",
			"session.create", "session.read", "session.update", "tier.create",
			"tier.read", "tier.update", "venue.read", "seating_plan.create",
			"seating_plan.read", "seating_plan.update.own",
			"event_session.assign_seating_plan", "media.write", "media.read",
			"import.bil24_session",
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := apikeys.ValidateScopes(tc.scopes)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateScopes(%v) = %v, want %v", tc.scopes, err, tc.wantErr)
			}
		})
	}
}

func TestGenerateRawKey_Shape(t *testing.T) {
	prefix, secret, raw, err := apikeys.GenerateRawKey()
	if err != nil {
		t.Fatalf("GenerateRawKey: %v", err)
	}
	if len(prefix) != apikeys.KeyPrefixLen {
		t.Errorf("prefix length = %d, want %d", len(prefix), apikeys.KeyPrefixLen)
	}
	if len(secret) != apikeys.KeySecretLen {
		t.Errorf("secret length = %d, want %d", len(secret), apikeys.KeySecretLen)
	}
	want := "ak_" + prefix + "_" + secret
	if raw != want {
		t.Errorf("raw = %q, want %q", raw, want)
	}
	if !strings.HasPrefix(raw, apikeys.KeyWirePrefix) {
		t.Errorf("raw = %q, want prefix %q", raw, apikeys.KeyWirePrefix)
	}
}

func TestGenerateRawKey_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		_, _, raw, err := apikeys.GenerateRawKey()
		if err != nil {
			t.Fatalf("GenerateRawKey: %v", err)
		}
		if seen[raw] {
			t.Fatalf("duplicate raw key generated: %q", raw)
		}
		seen[raw] = true
	}
}

func TestParseRawKey(t *testing.T) {
	prefix, secret, raw, err := apikeys.GenerateRawKey()
	if err != nil {
		t.Fatalf("GenerateRawKey: %v", err)
	}
	gotPrefix, gotSecret, err := apikeys.ParseRawKey(raw)
	if err != nil {
		t.Fatalf("ParseRawKey: %v", err)
	}
	if gotPrefix != prefix || gotSecret != secret {
		t.Errorf("ParseRawKey = (%q, %q), want (%q, %q)", gotPrefix, gotSecret, prefix, secret)
	}

	malformed := []string{
		"",
		"not-a-key",
		"ak_shortprefix_" + secret,    // prefix too short
		"ak_" + prefix + "_short",     // secret too short
		"xk_" + prefix + "_" + secret, // wrong leading token
		"ak_" + prefix,                // no secret segment
	}
	for _, m := range malformed {
		if _, _, err := apikeys.ParseRawKey(m); !errors.Is(err, apikeys.ErrMalformed) {
			t.Errorf("ParseRawKey(%q) err = %v, want ErrMalformed", m, err)
		}
	}
}

func TestIssue_RejectsForbiddenScopesAndBlankName(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	orgID, userID := uuid.New(), uuid.New()

	if _, _, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID: orgID, Name: "", Scopes: []string{"event.read"}, CreatedBy: userID,
	}); !errors.Is(err, apikeys.ErrNameRequired) {
		t.Errorf("blank name: err = %v, want ErrNameRequired", err)
	}

	if _, _, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID: orgID, Name: "site key", Scopes: []string{"admin.users"}, CreatedBy: userID,
	}); !errors.Is(err, apikeys.ErrForbiddenScope) {
		t.Errorf("forbidden scope: err = %v, want ErrForbiddenScope", err)
	}
}

func TestIssueThenAuthenticate_RoundTrip(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	orgID, userID := uuid.New(), uuid.New()

	key, raw, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID:     orgID,
		Name:      "lampyris-ops",
		Scopes:    []string{"event.read", "import.bil24_session"},
		CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if key.KeyHash == "" {
		t.Fatal("Issue: stored key has empty KeyHash")
	}
	if strings.Contains(key.KeyHash, raw) {
		t.Fatal("Issue: KeyHash must not contain the raw secret")
	}

	now := time.Now()
	got, err := apikeys.Authenticate(ctx, store, raw, now)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != key.ID {
		t.Errorf("Authenticate returned ID %v, want %v", got.ID, key.ID)
	}

	// Wrong secret against a valid prefix must fail distinctly.
	tamperedRaw := "ak_" + key.KeyPrefix + "_" + strings.Repeat("x", apikeys.KeySecretLen)
	if _, err := apikeys.Authenticate(ctx, store, tamperedRaw, now); !errors.Is(err, apikeys.ErrSecretMismatch) {
		t.Errorf("tampered secret: err = %v, want ErrSecretMismatch", err)
	}

	// Unknown prefix.
	unknownRaw := "ak_" + strings.Repeat("z", apikeys.KeyPrefixLen) + "_" + strings.Repeat("y", apikeys.KeySecretLen)
	if _, err := apikeys.Authenticate(ctx, store, unknownRaw, now); !errors.Is(err, apikeys.ErrNotFound) {
		t.Errorf("unknown prefix: err = %v, want ErrNotFound", err)
	}

	// Malformed input never reaches the store.
	if _, err := apikeys.Authenticate(ctx, store, "garbage", now); !errors.Is(err, apikeys.ErrMalformed) {
		t.Errorf("malformed: err = %v, want ErrMalformed", err)
	}
}

func TestAuthenticate_RevokedAndExpired(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	orgID, userID := uuid.New(), uuid.New()

	key, raw, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID: orgID, Name: "revocable", Scopes: []string{"event.read"}, CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	store.revoke(key.KeyPrefix)
	if _, err := apikeys.Authenticate(ctx, store, raw, time.Now()); !errors.Is(err, apikeys.ErrRevoked) {
		t.Errorf("revoked key: err = %v, want ErrRevoked", err)
	}

	expiresAt := time.Now().Add(time.Hour)
	key2, raw2, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID: orgID, Name: "expiring", Scopes: []string{"event.read"}, CreatedBy: userID,
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := apikeys.Authenticate(ctx, store, raw2, expiresAt.Add(time.Second)); !errors.Is(err, apikeys.ErrExpired) {
		t.Errorf("expired key: err = %v, want ErrExpired", err)
	}
	// Still valid just before expiry.
	if _, err := apikeys.Authenticate(ctx, store, raw2, expiresAt.Add(-time.Second)); err != nil {
		t.Errorf("not-yet-expired key: unexpected err = %v", err)
	}
	_ = key2
}

func TestTouchLastUsed_ThrottledToOncePerMinute(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	orgID, userID := uuid.New(), uuid.New()

	key, _, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID: orgID, Name: "throttled", Scopes: []string{"event.read"}, CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	t0 := time.Now()
	touched, err := apikeys.TouchLastUsed(ctx, store, key, t0)
	if err != nil {
		t.Fatalf("TouchLastUsed #1: %v", err)
	}
	if !touched {
		t.Fatal("first TouchLastUsed on a never-used key must write")
	}
	if len(store.touched[key.ID]) != 1 {
		t.Fatalf("expected 1 write, got %d", len(store.touched[key.ID]))
	}

	key.LastUsedAt = &t0
	touched, err = apikeys.TouchLastUsed(ctx, store, key, t0.Add(30*time.Second))
	if err != nil {
		t.Fatalf("TouchLastUsed #2: %v", err)
	}
	if touched {
		t.Error("TouchLastUsed within the throttle window must not write")
	}
	if len(store.touched[key.ID]) != 1 {
		t.Fatalf("expected still 1 write after throttled call, got %d", len(store.touched[key.ID]))
	}

	touched, err = apikeys.TouchLastUsed(ctx, store, key, t0.Add(61*time.Second))
	if err != nil {
		t.Fatalf("TouchLastUsed #3: %v", err)
	}
	if !touched {
		t.Error("TouchLastUsed after the throttle window must write")
	}
	if len(store.touched[key.ID]) != 2 {
		t.Fatalf("expected 2 writes, got %d", len(store.touched[key.ID]))
	}
}
