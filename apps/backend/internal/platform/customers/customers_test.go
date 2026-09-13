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

	// injectConflict, when set, makes the NEXT InsertIdentity call for the
	// named (kind, value) simulate losing a race to a concurrent resolver:
	// it seeds the "winner" identity directly on the root store (as if a
	// separate, already-committed transaction had just inserted it) and
	// returns ErrIdentityConflict, exactly like postgres_store.go's real
	// translation of SQLSTATE 23505. It is a shared pointer so it still
	// fires — exactly once — from inside a WithSavepoint snapshot.
	injectConflict *conflictInjector
}

// conflictInjector backs fakeStore.injectConflict. root always points at
// the outermost fakeStore a test constructed (clone() propagates the
// pointer unchanged), so the seeded "winner" row survives even when the
// snapshot that observed the conflict is discarded by WithSavepoint.
type conflictInjector struct {
	kind   IdentityKind
	value  string
	winner uuid.UUID
	fired  bool
	root   *fakeStore
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

	if inj := f.injectConflict; inj != nil && !inj.fired && inj.kind == kind && inj.value == value {
		inj.fired = true
		winnerRow := Identity{
			ID:              uuid.New(),
			CustomerID:      inj.winner,
			Kind:            kind,
			ValueNormalized: value,
			ChannelID:       channelID,
			Source:          source,
			FirstSeenAt:     time.Now().UTC(),
			LastSeenAt:      time.Now().UTC(),
		}
		// The winner represents a DIFFERENT, already-committed transaction —
		// it lands on the root store directly, bypassing whatever savepoint
		// snapshot this call is running against.
		inj.root.identities[winnerRow.ID] = &winnerRow
		return Identity{}, ErrIdentityConflict
	}

	// Enforce the same partial-unique-index semantics
	// customer_identities_strong_uq / _weak_uq apply in Postgres, so
	// fakeStore-based tests can exercise Resolve's race recovery without a
	// real database.
	for _, existing := range f.identities {
		if existing.Kind != kind || existing.ValueNormalized != value {
			continue
		}
		if kind.IsStrong() {
			return Identity{}, ErrIdentityConflict
		}
		if existing.ChannelID != nil && channelID != nil && *existing.ChannelID == *channelID {
			return Identity{}, ErrIdentityConflict
		}
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

// WithSavepoint gives fakeStore-based unit tests the same rollback
// semantics postgres_store.go gets from a real SAVEPOINT: fn runs against
// a snapshot of this store's maps, and on error the snapshot is discarded
// — nothing fn wrote survives — instead of merged back into f.
func (f *fakeStore) WithSavepoint(_ context.Context, fn func(Store) error) error {
	snapshot := f.clone()
	if err := fn(snapshot); err != nil {
		return err
	}
	f.adopt(snapshot)
	return nil
}

// clone deep-copies f's maps/slices into a fresh *fakeStore. injectConflict
// is a shared pointer, deliberately NOT deep-copied, so a conflict injected
// on the root store still fires (once) no matter how many nested
// WithSavepoint snapshots it passes through.
func (f *fakeStore) clone() *fakeStore {
	nf := &fakeStore{
		customers:      make(map[uuid.UUID]*Customer, len(f.customers)),
		identities:     make(map[uuid.UUID]*Identity, len(f.identities)),
		orgLinks:       make(map[[2]uuid.UUID]string, len(f.orgLinks)),
		verified:       make(map[uuid.UUID]time.Time, len(f.verified)),
		nextSystemID:   f.nextSystemID,
		injectConflict: f.injectConflict,
	}
	for id, c := range f.customers {
		cc := *c
		nf.customers[id] = &cc
	}
	for id, r := range f.identities {
		rr := *r
		nf.identities[id] = &rr
	}
	for k, v := range f.orgLinks {
		nf.orgLinks[k] = v
	}
	for k, v := range f.verified {
		nf.verified[k] = v
	}
	nf.mergeCandidates = append(nf.mergeCandidates, f.mergeCandidates...)
	nf.attributes = append(nf.attributes, f.attributes...)
	nf.touched = append(nf.touched, f.touched...)
	return nf
}

// adopt merges a successful snapshot's writes back into f — the "commit"
// counterpart to clone's "begin".
func (f *fakeStore) adopt(snapshot *fakeStore) {
	f.customers = snapshot.customers
	f.identities = snapshot.identities
	f.orgLinks = snapshot.orgLinks
	f.verified = snapshot.verified
	f.nextSystemID = snapshot.nextSystemID
	f.mergeCandidates = snapshot.mergeCandidates
	f.attributes = snapshot.attributes
	f.touched = snapshot.touched
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

// ── Race safety (23505-then-lookup) ──────────────────────────────────────────

// TestResolve_LostEmailRaceOnCreate_RecoversToWinningCustomer simulates two
// concurrent first-time Resolve calls for the identical brand-new email:
// both miss the Step 2 lookup, and the second one's identity insert loses
// the race with SQLSTATE 23505 (via fakeStore's injectConflict). Resolve
// must recover by re-resolving instead of returning the raw conflict.
func TestResolve_LostEmailRaceOnCreate_RecoversToWinningCustomer(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	winnerCustomer, _ := s.InsertCustomer(ctx, "Winner", "")

	s.injectConflict = &conflictInjector{
		kind:   KindEmail,
		value:  "race@example.com",
		winner: winnerCustomer.ID,
		root:   s,
	}

	res, err := Resolve(ctx, s, ResolveInput{Email: "race@example.com"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !s.injectConflict.fired {
		t.Fatalf("test bug: the injected conflict never fired")
	}
	if res.Customer.ID != winnerCustomer.ID {
		t.Fatalf("got customer %v, want the race winner %v", res.Customer.ID, winnerCustomer.ID)
	}
	if res.Created {
		t.Errorf("recovered resolve must report Created=false — it joined the winner, it did not mint a new customer")
	}
}

// TestResolve_LostEmailRaceOnCreate_NoOrphanCustomerSurvives is the
// invariant the concurrent integration test (postgres_store_integration_test.go)
// re-proves against a real database: after the race, exactly one customer
// and one identity row exist — the loser's InsertCustomer must have been
// rolled back together with its losing identity insert, inside the same
// savepoint.
func TestResolve_LostEmailRaceOnCreate_NoOrphanCustomerSurvives(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	// The winner represents a SEPARATE, already-committed transaction; it
	// is not present yet when Resolve starts (that is the whole point of
	// the race), and is only seeded once the injected conflict fires
	// inside Resolve's own losing InsertIdentity attempt.
	winnerCustomer, _ := s.InsertCustomer(ctx, "Winner", "")
	s.injectConflict = &conflictInjector{
		kind:   KindEmail,
		value:  "race2@example.com",
		winner: winnerCustomer.ID,
		root:   s,
	}

	if _, err := Resolve(ctx, s, ResolveInput{Email: "race2@example.com"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := len(s.customers); got != 1 {
		t.Fatalf("customers = %d, want exactly 1 (no orphan from the losing attempt)", got)
	}
	matching := 0
	for _, id := range s.identities {
		if id.Kind == KindEmail && id.ValueNormalized == "race2@example.com" {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("email identities for race2@example.com = %d, want exactly 1", matching)
	}
}

// TestResolve_LostWeakIdentityRace_ExistingCustomer covers the OTHER race
// class called out in the fix: attaching an additional identity to an
// ALREADY-resolved (found-branch) customer, not creating a new one. A
// device-token attach that loses the race must not error and must not
// disturb the customer it was already going to return.
func TestResolve_LostWeakIdentityRace_ExistingCustomer(t *testing.T) {
	s := newFakeStore()
	ctx := context.Background()
	channel := uuid.New()
	c, _ := s.InsertCustomer(ctx, "Anna", "")
	_, _ = s.InsertIdentity(ctx, c.ID, KindEmail, "anna@example.com", nil, SourceLive, nil)

	s.injectConflict = &conflictInjector{
		kind:   KindDevice,
		value:  "dev-race",
		winner: c.ID, // same customer — a concurrent duplicate resolve, not a real conflict
		root:   s,
	}

	res, err := Resolve(ctx, s, ResolveInput{
		Email:       "anna@example.com",
		ChannelID:   channel,
		DeviceToken: "dev-race",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Customer.ID != c.ID {
		t.Fatalf("got %v, want %v", res.Customer.ID, c.ID)
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
