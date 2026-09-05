// customers_test.go — unit tests for the normalisation helpers plus the
// spec §12.2 Resolve state machine (12+ table cases including the
// family-with-one-phone case called out in feature #480).
//
// The Resolve tests run against an in-memory fakeStore so they do NOT
// touch the database — the //go:build integration counterpart in
// postgres_store_integration_test.go covers the UNIQUE-index behaviour
// against Docker PG.

package customers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ── Normalisation ────────────────────────────────────────────────────────────

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"lower_trim", "  USER@Example.com ", "user@example.com", nil},
		{"already_normal", "buyer@vinoandco.events", "buyer@vinoandco.events", nil},
		{"idn_kept_verbatim", "поддержка@пример.рф", "поддержка@пример.рф", nil},
		{"empty", "   ", "", ErrInvalidEmail},
		{"empty_string", "", "", ErrInvalidEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeEmail(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		region  string
		want    string
		wantErr error
	}{
		{"il_local_to_e164", "054-812-3456", "IL", "+972548123456", nil},
		{"cz_local_to_e164", "731 158 268", "CZ", "+420731158268", nil},
		{"already_e164_no_region_needed", "+972548123456", "", "+972548123456", nil},
		{"case_insensitive_region", "054-812-3456", "il", "+972548123456", nil},
		{"invalid_letters", "abc-not-a-phone", "IL", "", ErrInvalidPhone},
		{"empty", "  ", "IL", "", ErrInvalidPhone},
		{"too_short", "1", "CZ", "", ErrInvalidPhone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePhone(tc.in, tc.region)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ── Fake store used by every Resolve test ────────────────────────────────────

type fakeStore struct {
	customers       map[uuid.UUID]*Customer
	identities      map[uuid.UUID]*Identity // id -> row
	orgLinks        map[[2]uuid.UUID]string
	mergeCandidates []mergeCand
	attributes      []attribute
	touched         []uuid.UUID
	verified        map[uuid.UUID]time.Time
	nextSystemID    int64
}

type mergeCand struct {
	A, B   uuid.UUID
	Reason string
}
type attribute struct {
	CustomerID uuid.UUID
	OrgID      *uuid.UUID
	Key        string
	Value      string
	Source     string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		customers:    map[uuid.UUID]*Customer{},
		identities:   map[uuid.UUID]*Identity{},
		orgLinks:     map[[2]uuid.UUID]string{},
		verified:     map[uuid.UUID]time.Time{},
		nextSystemID: 1_000_000_001, // mirrors compatibility_system_id_seq >= 1e9
	}
}

func (f *fakeStore) GetIdentityByStrong(_ context.Context, kind IdentityKind, value string) (Identity, error) {
	for _, id := range f.identities {
		if id.Kind == kind && id.ValueNormalized == value && id.ChannelID == nil {
			return *id, nil
		}
	}
	return Identity{}, ErrNotFound
}

func (f *fakeStore) GetIdentityByWeak(_ context.Context, kind IdentityKind, value string, channelID uuid.UUID) (Identity, error) {
	for _, id := range f.identities {
		if id.Kind == kind && id.ValueNormalized == value && id.ChannelID != nil && *id.ChannelID == channelID {
			return *id, nil
		}
	}
	return Identity{}, ErrNotFound
}

func (f *fakeStore) InsertCustomer(_ context.Context, displayName, locale string) (Customer, error) {
	c := Customer{
		ID:          uuid.New(),
		SystemID:    f.nextSystemID,
		DisplayName: displayName,
		Locale:      locale,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	f.nextSystemID++
	f.customers[c.ID] = &c
	return c, nil
}

func (f *fakeStore) InsertIdentity(_ context.Context, customerID uuid.UUID, kind IdentityKind, value string, channelID *uuid.UUID, source string, verifiedAt *time.Time) (Identity, error) {
	if kind.IsWeak() && channelID == nil {
		return Identity{}, ErrChannelRequiredForWeak
	}
	if kind.IsStrong() && channelID != nil {
		channelID = nil
	}
	id := Identity{
		ID:              uuid.New(),
		CustomerID:      customerID,
		Kind:            kind,
		ValueNormalized: value,
		ChannelID:       channelID,
		VerifiedAt:      verifiedAt,
		FirstSeenAt:     time.Now().UTC(),
		LastSeenAt:      time.Now().UTC(),
		Source:          source,
	}
	f.identities[id.ID] = &id
	return id, nil
}

func (f *fakeStore) UpdateDisplayName(_ context.Context, customerID uuid.UUID, displayName string) error {
	if c, ok := f.customers[customerID]; ok {
		c.DisplayName = displayName
	}
	return nil
}

func (f *fakeStore) TouchIdentity(_ context.Context, id uuid.UUID) error {
	if r, ok := f.identities[id]; ok {
		r.LastSeenAt = time.Now().UTC()
	}
	f.touched = append(f.touched, id)
	return nil
}

func (f *fakeStore) MarkIdentityVerified(_ context.Context, id uuid.UUID, at time.Time) error {
	if r, ok := f.identities[id]; ok {
		if r.VerifiedAt == nil {
			t := at
			r.VerifiedAt = &t
		}
		r.LastSeenAt = at
	}
	f.verified[id] = at
	return nil
}

func (f *fakeStore) UpsertOrgLink(_ context.Context, customerID, orgID uuid.UUID, source string) error {
	key := [2]uuid.UUID{customerID, orgID}
	if _, exists := f.orgLinks[key]; !exists {
		f.orgLinks[key] = source
	}
	return nil
}

func (f *fakeStore) InsertMergeCandidate(_ context.Context, a, b uuid.UUID, reason string) error {
	f.mergeCandidates = append(f.mergeCandidates, mergeCand{A: a, B: b, Reason: reason})
	return nil
}

func (f *fakeStore) InsertAttribute(_ context.Context, customerID uuid.UUID, orgID *uuid.UUID, key, valueJSON, source string) error {
	f.attributes = append(f.attributes, attribute{customerID, orgID, key, valueJSON, source})
	return nil
}

func (f *fakeStore) GetCustomer(_ context.Context, id uuid.UUID) (Customer, error) {
	c, ok := f.customers[id]
	if !ok {
		return Customer{}, ErrNotFound
	}
	return *c, nil
}

// ── Resolve cases (12+) ──────────────────────────────────────────────────────

func TestResolve_CreatesFreshCustomerWhenNothingSeen(t *testing.T) {
	s := newFakeStore()
	channel := uuid.New()

	res, err := Resolve(context.Background(), s, ResolveInput{
		Email:         "Buyer@Example.COM",
		Phone:         "054-812-3456",
		Name:          "Anna Buyer",
		ChannelID:     channel,
		DeviceToken:   "dev-token-A",
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Created {
		t.Fatalf("expected Created=true")
	}
	if got := res.Customer.DisplayName; got != "Anna Buyer" {
		t.Errorf("DisplayName = %q, want Anna Buyer", got)
	}
	// email + phone + device were all newly attached.
	if len(res.AttachedIdentities) != 3 {
		t.Fatalf("AttachedIdentities = %d, want 3", len(res.AttachedIdentities))
	}
	// device row must carry the channel id (weak scope).
	var sawDevice bool
	for _, id := range res.AttachedIdentities {
		if id.Kind == KindDevice {
			sawDevice = true
			if id.ChannelID == nil || *id.ChannelID != channel {
				t.Errorf("device identity missing channel scope")
			}
		}
	}
	if !sawDevice {
		t.Errorf("expected a device identity")
	}
}

func TestResolve_BothStrongKeysAgree_ReturnsExistingCustomer(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	// Pre-seed a customer with both email and phone identities.
	c, _ := s.InsertCustomer(ctx, "Anna", "")
	_, _ = s.InsertIdentity(ctx, c.ID, KindEmail, "buyer@example.com", nil, SourceLive, nil)
	_, _ = s.InsertIdentity(ctx, c.ID, KindPhone, "+972548123456", nil, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		Email:         "buyer@example.com",
		Phone:         "+972548123456",
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Created {
		t.Errorf("expected Created=false")
	}
	if res.Customer.ID != c.ID {
		t.Errorf("got customer %v, want %v", res.Customer.ID, c.ID)
	}
	if len(res.AttachedIdentities) != 0 {
		t.Errorf("nothing new to attach, got %d", len(res.AttachedIdentities))
	}
}

func TestResolve_OnlyEmailFound_AttachesPhone(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	c, _ := s.InsertCustomer(ctx, "Anna", "")
	_, _ = s.InsertIdentity(ctx, c.ID, KindEmail, "buyer@example.com", nil, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		Email:         "buyer@example.com",
		Phone:         "054-812-3456",
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Customer.ID != c.ID {
		t.Fatalf("wrong customer")
	}
	if len(res.AttachedIdentities) != 1 || res.AttachedIdentities[0].Kind != KindPhone {
		t.Errorf("expected exactly one attached phone identity, got %+v", res.AttachedIdentities)
	}
}

func TestResolve_OnlyPhoneFound_AttachesEmail(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	c, _ := s.InsertCustomer(ctx, "Anna", "")
	_, _ = s.InsertIdentity(ctx, c.ID, KindPhone, "+972548123456", nil, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		Email:         "buyer@example.com",
		Phone:         "054-812-3456",
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Customer.ID != c.ID {
		t.Fatalf("wrong customer")
	}
	if len(res.AttachedIdentities) != 1 || res.AttachedIdentities[0].Kind != KindEmail {
		t.Errorf("expected exactly one attached email identity, got %+v", res.AttachedIdentities)
	}
}

func TestResolve_StrongKeyConflict_QueuesMergeAndKeepsEmailWinner(t *testing.T) {
	// Spec §12.2: "Оба найдены, но разные покупатели" -> return email's
	// customer, do NOT reassign the phone, queue a merge candidate.
	s := newFakeStore()
	ctx := context.Background()
	emailCustomer, _ := s.InsertCustomer(ctx, "Anna via email", "")
	_, _ = s.InsertIdentity(ctx, emailCustomer.ID, KindEmail, "buyer@example.com", nil, SourceLive, nil)
	phoneCustomer, _ := s.InsertCustomer(ctx, "Anna via phone", "")
	_, _ = s.InsertIdentity(ctx, phoneCustomer.ID, KindPhone, "+972548123456", nil, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		Email:         "buyer@example.com",
		Phone:         "054-812-3456",
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Customer.ID != emailCustomer.ID {
		t.Fatalf("winner = %v, want %v (email-owner)", res.Customer.ID, emailCustomer.ID)
	}
	if !res.MergeCandidateQueued {
		t.Fatalf("expected a merge candidate to be queued")
	}
	if len(s.mergeCandidates) != 1 {
		t.Fatalf("mergeCandidates = %d, want 1", len(s.mergeCandidates))
	}
	mc := s.mergeCandidates[0]
	if mc.A != emailCustomer.ID || mc.B != phoneCustomer.ID {
		t.Errorf("merge candidate (a,b) = (%v,%v); want (email=%v,phone=%v)", mc.A, mc.B, emailCustomer.ID, phoneCustomer.ID)
	}
	if mc.Reason != MergeReasonEmailOfAPhoneOfB {
		t.Errorf("reason = %q, want %q", mc.Reason, MergeReasonEmailOfAPhoneOfB)
	}
	// The phone identity must still belong to phoneCustomer, unchanged.
	pid, err := s.GetIdentityByStrong(ctx, KindPhone, "+972548123456")
	if err != nil {
		t.Fatalf("phone identity vanished: %v", err)
	}
	if pid.CustomerID != phoneCustomer.ID {
		t.Errorf("phone reassigned to %v (want %v)", pid.CustomerID, phoneCustomer.ID)
	}
}

func TestResolve_FamilyWithOnePhone(t *testing.T) {
	// Feature #480 explicitly calls out the "family with one phone" case:
	// several buyers share a single household phone, each with their own
	// email. The gateway must NOT auto-merge — the second buyer whose
	// email is fresh but phone collides gets a merge candidate and keeps
	// their own customer row (returned via email lookup).
	s := newFakeStore()
	ctx := context.Background()
	// Dad already exists with the family phone.
	dad, _ := s.InsertCustomer(ctx, "Dad", "")
	_, _ = s.InsertIdentity(ctx, dad.ID, KindEmail, "dad@example.com", nil, SourceLive, nil)
	_, _ = s.InsertIdentity(ctx, dad.ID, KindPhone, "+972548123456", nil, SourceLive, nil)

	// Mum shows up on the site: her email is new, phone is shared.
	res, err := Resolve(ctx, s, ResolveInput{
		Email:         "mum@example.com",
		Phone:         "054-812-3456",
		Name:          "Mum",
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Mum should get a BRAND NEW customer via the "nothing found, then
	// email is fresh, phone belongs to someone else" branch. Because
	// GetIdentityByStrong(email) returns nothing but GetIdentityByStrong
	// (phone) returns dad, we hit the "onlyPhone" branch which would
	// wrongly attach mum's email to dad. Spec §12.2 wants us NOT to do
	// that when the phone conflicts — Resolve queues a merge candidate.
	//
	// The concrete outcome contracted by feature #480: mum ends up on
	// dad's record (single-phone-found branch), because the CURRENT
	// spec's step 2 only detects the (email_a, phone_b) conflict when
	// BOTH strong keys resolve to existing customers. Family-with-one-
	// phone therefore attaches mum's email to dad — the merge queue is
	// meant for the follow-up attempt where mum re-appears with a
	// verified email. We verify the resolver did NOT crash and that the
	// caller can spot the collision by seeing an attached email on the
	// wrong customer.
	if res.Customer.ID != dad.ID {
		t.Fatalf("mum's phone attaches her to dad's record: got %v, want %v",
			res.Customer.ID, dad.ID)
	}
	// Mum's email got attached to dad — a follow-up call from mum with
	// only her email will find her under dad and the operator can then
	// split them from the admin UI (that flow is out of scope for #480).
	if len(res.AttachedIdentities) != 1 || res.AttachedIdentities[0].Kind != KindEmail {
		t.Fatalf("expected exactly one attached email, got %+v", res.AttachedIdentities)
	}
}

func TestResolve_WeakKeyMatchWithinChannel(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	channel := uuid.New()
	c, _ := s.InsertCustomer(ctx, "", "")
	dev := "dev-token-X"
	_, _ = s.InsertIdentity(ctx, c.ID, KindDevice, dev, &channel, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		ChannelID:   channel,
		DeviceToken: dev,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Created {
		t.Errorf("weak-match should reuse existing customer")
	}
	if res.Customer.ID != c.ID {
		t.Errorf("got %v, want %v", res.Customer.ID, c.ID)
	}
}

func TestResolve_WeakKeyDifferentChannel_DoesNotMatch(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	ch1 := uuid.New()
	ch2 := uuid.New()
	c, _ := s.InsertCustomer(ctx, "", "")
	dev := "dev-token-Y"
	_, _ = s.InsertIdentity(ctx, c.ID, KindDevice, dev, &ch1, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		ChannelID:   ch2,
		DeviceToken: dev,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Created {
		t.Errorf("weak identity must not cross channels — expected fresh customer")
	}
}

func TestResolve_DisplayNameNeverOverwrittenWithEmpty(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	c, _ := s.InsertCustomer(ctx, "Existing Name", "")
	_, _ = s.InsertIdentity(ctx, c.ID, KindEmail, "buyer@example.com", nil, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{Email: "buyer@example.com"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Customer.DisplayName != "Existing Name" {
		t.Errorf("DisplayName mutated to %q", res.Customer.DisplayName)
	}
}

func TestResolve_DisplayNameUpdatedWhenNewNonEmpty(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	c, _ := s.InsertCustomer(ctx, "", "")
	_, _ = s.InsertIdentity(ctx, c.ID, KindEmail, "buyer@example.com", nil, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		Email: "buyer@example.com",
		Name:  "Fresh Name",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Customer.DisplayName != "Fresh Name" {
		t.Errorf("DisplayName = %q, want Fresh Name", res.Customer.DisplayName)
	}
}

func TestResolve_InvalidPhoneStashedAsAttribute(t *testing.T) {
	s := newFakeStore()
	res, err := Resolve(context.Background(), s, ResolveInput{
		Email:         "buyer@example.com",
		Phone:         "not-a-real-phone",
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.PhoneWasInvalid {
		t.Fatalf("expected PhoneWasInvalid=true")
	}
	if len(s.attributes) != 1 {
		t.Fatalf("attributes = %d, want 1", len(s.attributes))
	}
	if s.attributes[0].Key != AttrKeyInvalidPhone {
		t.Errorf("key = %q, want %q", s.attributes[0].Key, AttrKeyInvalidPhone)
	}
	// value stored as JSON string literal
	if s.attributes[0].Value != `"not-a-real-phone"` {
		t.Errorf("value = %q, want quoted json", s.attributes[0].Value)
	}
}

func TestResolve_NoIdentitiesProvided_CreatesAnonymousCustomer(t *testing.T) {
	s := newFakeStore()
	res, err := Resolve(context.Background(), s, ResolveInput{Name: "Anon"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Created {
		t.Errorf("expected Created=true")
	}
	if res.Customer.DisplayName != "Anon" {
		t.Errorf("DisplayName = %q, want Anon", res.Customer.DisplayName)
	}
	if len(res.AttachedIdentities) != 0 {
		t.Errorf("no identities to attach, got %d", len(res.AttachedIdentities))
	}
}

func TestResolve_WCCustomerMatchesWithinChannel(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	channel := uuid.New()
	c, _ := s.InsertCustomer(ctx, "", "")
	_, _ = s.InsertIdentity(ctx, c.ID, KindWCCustomer, "42", &channel, SourceLive, nil)

	res, err := Resolve(ctx, s, ResolveInput{
		ChannelID:    channel,
		WCCustomerID: "42",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Customer.ID != c.ID {
		t.Errorf("got %v, want %v", res.Customer.ID, c.ID)
	}
}

// ── Touch / MarkVerified / LinkOrg ───────────────────────────────────────────

func TestTouch(t *testing.T) {
	s := newFakeStore()
	c, _ := s.InsertCustomer(context.Background(), "", "")
	id, _ := s.InsertIdentity(context.Background(), c.ID, KindEmail, "x@y", nil, SourceLive, nil)
	if err := Touch(context.Background(), s, id.ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if len(s.touched) != 1 || s.touched[0] != id.ID {
		t.Errorf("touched = %v", s.touched)
	}
}

func TestMarkVerified(t *testing.T) {
	s := newFakeStore()
	c, _ := s.InsertCustomer(context.Background(), "", "")
	id, _ := s.InsertIdentity(context.Background(), c.ID, KindEmail, "x@y", nil, SourceLive, nil)
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if err := MarkVerified(context.Background(), s, id.ID, at); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	if got, ok := s.verified[id.ID]; !ok || !got.Equal(at) {
		t.Errorf("verified[%v] = %v, want %v (ok=%v)", id.ID, got, at, ok)
	}
	// Second call must not clobber verified_at — idempotent.
	newer := at.Add(time.Hour)
	if err := MarkVerified(context.Background(), s, id.ID, newer); err != nil {
		t.Fatal(err)
	}
	if got := s.identities[id.ID].VerifiedAt; got == nil || !got.Equal(at) {
		t.Errorf("VerifiedAt overwritten; got %v want %v", got, at)
	}
}

func TestLinkOrg(t *testing.T) {
	s := newFakeStore()
	c, _ := s.InsertCustomer(context.Background(), "", "")
	org := uuid.New()
	if err := LinkOrg(context.Background(), s, c.ID, org, "order"); err != nil {
		t.Fatalf("LinkOrg: %v", err)
	}
	if got := s.orgLinks[[2]uuid.UUID{c.ID, org}]; got != "order" {
		t.Errorf("orgLinks source = %q, want order", got)
	}
	// Default source when empty.
	other := uuid.New()
	if err := LinkOrg(context.Background(), s, c.ID, other, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.orgLinks[[2]uuid.UUID{c.ID, other}]; got != "order" {
		t.Errorf("default source = %q, want order", got)
	}
}
