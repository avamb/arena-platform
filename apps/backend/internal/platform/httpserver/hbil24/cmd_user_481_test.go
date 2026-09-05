// cmd_user_481_test.go — unit tests for feature #481 (W1-A4c, spec §7.3):
// CREATE_USER and the shared requireGatewaySession guard (result code 1).
//
// Everything runs against in-memory fakes — no PostgreSQL. The live
// round-trip (real customers/gateway_sessions tables) belongs to the
// integration harness in tests/compat/bil24.

package hbil24

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fakes
// ─────────────────────────────────────────────────────────────────────────────

// memCustomerStore is an in-memory customers.Store good enough for the §12.2
// paths CREATE_USER exercises: strong-key (email/phone) resolution plus fresh
// customer creation. Counters/merge candidates are accepted and dropped.
type memCustomerStore struct {
	custs        map[uuid.UUID]customers.Customer
	strong       map[string]customers.Identity
	weak         map[string]customers.Identity
	nextSystemID int64
}

func newMemCustomerStore() *memCustomerStore {
	return &memCustomerStore{
		custs:        map[uuid.UUID]customers.Customer{},
		strong:       map[string]customers.Identity{},
		weak:         map[string]customers.Identity{},
		nextSystemID: 1000000001,
	}
}

func strongKey(kind customers.IdentityKind, value string) string {
	return string(kind) + "|" + value
}

func weakKey(kind customers.IdentityKind, value string, channelID uuid.UUID) string {
	return string(kind) + "|" + value + "|" + channelID.String()
}

func (m *memCustomerStore) GetIdentityByStrong(_ context.Context, kind customers.IdentityKind, value string) (customers.Identity, error) {
	if id, ok := m.strong[strongKey(kind, value)]; ok {
		return id, nil
	}
	return customers.Identity{}, customers.ErrNotFound
}

func (m *memCustomerStore) GetIdentityByWeak(_ context.Context, kind customers.IdentityKind, value string, channelID uuid.UUID) (customers.Identity, error) {
	if id, ok := m.weak[weakKey(kind, value, channelID)]; ok {
		return id, nil
	}
	return customers.Identity{}, customers.ErrNotFound
}

func (m *memCustomerStore) InsertCustomer(_ context.Context, displayName, locale string) (customers.Customer, error) {
	c := customers.Customer{
		ID:          uuid.New(),
		SystemID:    m.nextSystemID,
		DisplayName: displayName,
		Locale:      locale,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	m.nextSystemID++
	m.custs[c.ID] = c
	return c, nil
}

func (m *memCustomerStore) InsertIdentity(_ context.Context, customerID uuid.UUID, kind customers.IdentityKind, value string, channelID *uuid.UUID, source string, verifiedAt *time.Time) (customers.Identity, error) {
	id := customers.Identity{
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
	if channelID == nil {
		m.strong[strongKey(kind, value)] = id
	} else {
		m.weak[weakKey(kind, value, *channelID)] = id
	}
	return id, nil
}

func (m *memCustomerStore) UpdateDisplayName(_ context.Context, customerID uuid.UUID, displayName string) error {
	c, ok := m.custs[customerID]
	if !ok {
		return customers.ErrNotFound
	}
	c.DisplayName = displayName
	m.custs[customerID] = c
	return nil
}

func (m *memCustomerStore) TouchIdentity(context.Context, uuid.UUID) error { return nil }

func (m *memCustomerStore) MarkIdentityVerified(context.Context, uuid.UUID, time.Time) error {
	return nil
}

func (m *memCustomerStore) UpsertOrgLink(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (m *memCustomerStore) InsertMergeCandidate(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (m *memCustomerStore) InsertAttribute(context.Context, uuid.UUID, *uuid.UUID, string, string, string) error {
	return nil
}

func (m *memCustomerStore) GetCustomer(_ context.Context, id uuid.UUID) (customers.Customer, error) {
	if c, ok := m.custs[id]; ok {
		return c, nil
	}
	return customers.Customer{}, customers.ErrNotFound
}

// memSessionQuerier is an in-memory GatewaySessionQuerier.
type memSessionQuerier struct {
	byToken    map[string]gen.GatewaySessionRow
	bySystemID map[int64]gen.CustomerRow
	extended   map[uuid.UUID]time.Time
	insertErr  error
	lookupErr  error
}

func newMemSessionQuerier() *memSessionQuerier {
	return &memSessionQuerier{
		byToken:    map[string]gen.GatewaySessionRow{},
		bySystemID: map[int64]gen.CustomerRow{},
		extended:   map[uuid.UUID]time.Time{},
	}
}

func (m *memSessionQuerier) InsertGatewaySession(
	_ context.Context,
	sessionToken string,
	customerID, orgID, channelID uuid.UUID,
	locale string,
	promoCodes []string,
	expiresAt time.Time,
) (gen.GatewaySessionRow, error) {
	if m.insertErr != nil {
		return gen.GatewaySessionRow{}, m.insertErr
	}
	row := gen.GatewaySessionRow{
		ID:           uuid.New(),
		SessionToken: sessionToken,
		CustomerID:   customerID,
		OrgID:        orgID,
		ChannelID:    channelID,
		Locale:       locale,
		PromoCodes:   promoCodes,
		CreatedAt:    time.Now().UTC(),
		LastSeenAt:   time.Now().UTC(),
		ExpiresAt:    expiresAt,
	}
	m.byToken[sessionToken] = row
	return row, nil
}

func (m *memSessionQuerier) GetGatewaySessionByToken(_ context.Context, token string) (gen.GatewaySessionRow, error) {
	if m.lookupErr != nil {
		return gen.GatewaySessionRow{}, m.lookupErr
	}
	if row, ok := m.byToken[token]; ok {
		return row, nil
	}
	return gen.GatewaySessionRow{}, pgx.ErrNoRows
}

func (m *memSessionQuerier) ExtendGatewaySessionExpiry(_ context.Context, id uuid.UUID, expiresAt time.Time) error {
	m.extended[id] = expiresAt
	return nil
}

func (m *memSessionQuerier) GetCustomerBySystemID(_ context.Context, systemID int64) (gen.CustomerRow, error) {
	if row, ok := m.bySystemID[systemID]; ok {
		return row, nil
	}
	return gen.CustomerRow{}, pgx.ErrNoRows
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixture helpers
// ─────────────────────────────────────────────────────────────────────────────

const createUserToken = "s3cret"

func createUserChannel(t *testing.T) gen.SalesChannelRow {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(createUserToken), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	settings, err := json.Marshal(map[string]any{
		"gateway": map[string]any{
			"enabled": true, "token_hash": string(hash), "default_locale": "cs",
		},
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	return gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 1271, Settings: settings,
	}
}

func buildUserHandler(t *testing.T, ch gen.SalesChannelRow, sq GatewaySessionQuerier, cs customers.Store) *Handler {
	t.Helper()
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, ReservationDeps{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	h = h.WithChannelLookup(&fakeChannelLookup{
		byDisplayNumber: map[int64]gen.SalesChannelRow{ch.DisplayNumber: ch},
		byUUID:          map[uuid.UUID]gen.SalesChannelRow{ch.ID: ch},
	}).WithRequireToken(true)
	if sq != nil {
		h = h.WithGatewaySessions(sq)
	}
	if cs != nil {
		h = h.WithCustomerStore(cs)
	}
	return h
}

// createUserEnvelope decodes the flat Bil24 envelope CREATE_USER writes.
type createUserEnvelope struct {
	ResultCode  int    `json:"resultCode"`
	Description string `json:"description"`
	Command     string `json:"command"`
	UserID      int64  `json:"userId"`
	SessionID   string `json:"sessionId"`
}

func postCreateUser(t *testing.T, h *Handler, req bil24Request) createUserEnvelope {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/compat/bil24/json", strings.NewReader("{}"))
	h.handleBil24CreateUser(w, r, req)
	var env createUserEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, w.Body.String())
	}
	return env
}

func createUserRequest() bil24Request {
	return bil24Request{
		Command:   "CREATE_USER",
		FID:       "1271",
		Token:     createUserToken,
		Locale:    "ru-RU",
		Email:     "buyer@example.com",
		FirstName: "Anna",
		LastName:  "Novak",
		Phone:     "+420123456789",
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CREATE_USER
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_481_CreateUser_HappyPath(t *testing.T) {
	ch := createUserChannel(t)
	sq := newMemSessionQuerier()
	cs := newMemCustomerStore()
	h := buildUserHandler(t, ch, sq, cs)

	env := postCreateUser(t, h, createUserRequest())

	if env.ResultCode != ResultCodeOK {
		t.Fatalf("resultCode = %d, want 0 (desc=%q)", env.ResultCode, env.Description)
	}
	if env.Command != "CREATE_USER" {
		t.Errorf("command = %q, want CREATE_USER", env.Command)
	}
	// Spec §3.1: userId is customers.system_id — a bigint, never a UUID.
	if env.UserID < 1000000000 {
		t.Errorf("userId = %d, want a compatibility system_id (>= 1e9)", env.UserID)
	}
	// 32 crypto/rand bytes in unpadded base64url = exactly 43 characters.
	if len(env.SessionID) != 43 {
		t.Errorf("sessionId = %q (len %d), want 43 chars of base64url",
			env.SessionID, len(env.SessionID))
	}

	row, ok := sq.byToken[env.SessionID]
	if !ok {
		t.Fatalf("no gateway_sessions row persisted for %q", env.SessionID)
	}
	if row.OrgID != ch.OrgID || row.ChannelID != ch.ID {
		t.Errorf("session scoped to (%s,%s), want (%s,%s)",
			row.OrgID, row.ChannelID, ch.OrgID, ch.ID)
	}
	if row.Locale != "ru" {
		t.Errorf("session locale = %q, want the negotiated %q", row.Locale, "ru")
	}
	// expires_at = now + 30d (spec §7.3), allow a generous clock window.
	want := time.Now().UTC().Add(DefaultGatewaySessionTTL)
	if delta := row.ExpiresAt.Sub(want); delta > time.Minute || delta < -time.Minute {
		t.Errorf("expires_at = %s, want ≈ %s (now+30d)", row.ExpiresAt, want)
	}

	// display_name = firstName + " " + lastName (spec §7.3).
	cust, err := cs.GetCustomer(context.Background(), row.CustomerID)
	if err != nil {
		t.Fatalf("customer %s missing from store: %v", row.CustomerID, err)
	}
	if cust.DisplayName != "Anna Novak" {
		t.Errorf("display_name = %q, want %q", cust.DisplayName, "Anna Novak")
	}
	if cust.SystemID != env.UserID {
		t.Errorf("userId %d != customers.system_id %d", env.UserID, cust.SystemID)
	}
}

// TestBil24_481_CreateUser_SameEmailSameUserNewSession pins the contract the
// same_email_new_session golden encodes: the strong email key resolves to the
// SAME customer, but the command always mints a fresh session.
func TestBil24_481_CreateUser_SameEmailSameUserNewSession(t *testing.T) {
	ch := createUserChannel(t)
	sq := newMemSessionQuerier()
	h := buildUserHandler(t, ch, sq, newMemCustomerStore())

	first := postCreateUser(t, h, createUserRequest())
	second := postCreateUser(t, h, createUserRequest())

	if first.ResultCode != ResultCodeOK || second.ResultCode != ResultCodeOK {
		t.Fatalf("both calls must succeed; got %d and %d", first.ResultCode, second.ResultCode)
	}
	if first.UserID != second.UserID {
		t.Errorf("same email resolved to different userIds: %d vs %d (spec §12.2)",
			first.UserID, second.UserID)
	}
	if first.SessionID == second.SessionID {
		t.Errorf("CREATE_USER reused sessionId %q; spec §7.3 mints a NEW session",
			first.SessionID)
	}
	if len(sq.byToken) != 2 {
		t.Errorf("expected 2 gateway_sessions rows, got %d", len(sq.byToken))
	}
}

// A request with no email and no phone is legal: it creates a brand-new
// anonymous buyer (spec §7.3).
func TestBil24_481_CreateUser_AnonymousCreatesNewCustomerEachTime(t *testing.T) {
	ch := createUserChannel(t)
	h := buildUserHandler(t, ch, newMemSessionQuerier(), newMemCustomerStore())

	req := bil24Request{Command: "CREATE_USER", FID: "1271", Token: createUserToken}
	first := postCreateUser(t, h, req)
	second := postCreateUser(t, h, req)

	if first.ResultCode != ResultCodeOK || second.ResultCode != ResultCodeOK {
		t.Fatalf("anonymous CREATE_USER must succeed; got %d/%d",
			first.ResultCode, second.ResultCode)
	}
	if first.UserID == second.UserID {
		t.Errorf("keyless CREATE_USER must mint a NEW buyer each time; both got %d",
			first.UserID)
	}
}

func TestBil24_481_CreateUser_BadToken_Returns4(t *testing.T) {
	ch := createUserChannel(t)
	h := buildUserHandler(t, ch, newMemSessionQuerier(), newMemCustomerStore())

	req := createUserRequest()
	req.Token = "wrong"
	if got := postCreateUser(t, h, req).ResultCode; got != ResultCodeUnauthorized {
		t.Errorf("resultCode = %d, want %d for a bad token", got, ResultCodeUnauthorized)
	}
}

// Without a fid there is no (org, channel) to scope the session to, so
// CREATE_USER refuses even when the token gate is off.
func TestBil24_481_CreateUser_UnknownFID_Returns4_WithoutTokenGate(t *testing.T) {
	ch := createUserChannel(t)
	h := buildUserHandler(t, ch, newMemSessionQuerier(), newMemCustomerStore()).
		WithRequireToken(false)

	req := createUserRequest()
	req.FID = "9999"
	env := postCreateUser(t, h, req)
	if env.ResultCode != ResultCodeUnauthorized {
		t.Errorf("resultCode = %d, want %d for an unknown fid", env.ResultCode, ResultCodeUnauthorized)
	}
	if env.Description == "" {
		t.Error("the -4 envelope must carry a description")
	}
}

// Deployments that have not wired the customers/session surface must not
// pretend to succeed — they self-gate with -99 (nil-dependency convention).
func TestBil24_481_CreateUser_NoDeps_Returns99(t *testing.T) {
	ch := createUserChannel(t)
	for _, tc := range []struct {
		name string
		sq   GatewaySessionQuerier
		cs   customers.Store
	}{
		{"no_sessions", nil, newMemCustomerStore()},
		{"no_customers", newMemSessionQuerier(), nil},
		{"neither", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := buildUserHandler(t, ch, tc.sq, tc.cs)
			if got := postCreateUser(t, h, createUserRequest()).ResultCode; got != ResultCodeInternalError {
				t.Errorf("resultCode = %d, want %d", got, ResultCodeInternalError)
			}
		})
	}
}

func TestBil24_481_CreateUser_SessionInsertFails_ReturnsTransient(t *testing.T) {
	ch := createUserChannel(t)
	sq := newMemSessionQuerier()
	sq.insertErr = fmt.Errorf("connection reset")
	h := buildUserHandler(t, ch, sq, newMemCustomerStore())

	env := postCreateUser(t, h, createUserRequest())
	if env.ResultCode != ResultCodeTransient {
		t.Errorf("resultCode = %d, want %d (transient)", env.ResultCode, ResultCodeTransient)
	}
	if env.SessionID != "" {
		t.Errorf("a failed insert must not leak a sessionId; got %q", env.SessionID)
	}
}

func TestBil24_481_NewGatewaySessionToken_IsUnique43CharBase64URL(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 64; i++ {
		tok, err := newGatewaySessionToken()
		if err != nil {
			t.Fatalf("newGatewaySessionToken: %v", err)
		}
		if len(tok) != 43 {
			t.Fatalf("token %q has len %d, want 43", tok, len(tok))
		}
		if strings.ContainsAny(tok, "+/=") {
			t.Fatalf("token %q must be unpadded base64url (no + / =)", tok)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token %q after %d draws", tok, i)
		}
		seen[tok] = struct{}{}
	}
}

func TestBil24_481_Bil24DisplayName(t *testing.T) {
	cases := []struct{ first, last, want string }{
		{"Anna", "Novak", "Anna Novak"},
		{"  Anna ", "  Novak  ", "Anna Novak"},
		{"", "Novak", "Novak"},
		{"Anna", "", "Anna"},
		{"", "", ""},
		{"   ", "   ", ""},
	}
	for _, tc := range cases {
		if got := bil24DisplayName(tc.first, tc.last); got != tc.want {
			t.Errorf("bil24DisplayName(%q,%q) = %q, want %q", tc.first, tc.last, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// requireGatewaySession (spec §7.4 — result code 1)
// ─────────────────────────────────────────────────────────────────────────────

// seedSession registers a valid (session, customer) pair in the fake and
// returns the wire values a command would carry.
func seedSession(sq *memSessionQuerier, ch gen.SalesChannelRow, expiresAt time.Time) (token string, userID int64, sessionRowID uuid.UUID) {
	customerID := uuid.New()
	token = "tok-" + uuid.NewString()
	userID = 1000000042
	row := gen.GatewaySessionRow{
		ID:           uuid.New(),
		SessionToken: token,
		CustomerID:   customerID,
		OrgID:        ch.OrgID,
		ChannelID:    ch.ID,
		Locale:       "cs",
		ExpiresAt:    expiresAt,
	}
	sq.byToken[token] = row
	sq.bySystemID[userID] = gen.CustomerRow{ID: customerID, SystemID: userID}
	return token, userID, row.ID
}

func runSessionGuard(t *testing.T, h *Handler, req bil24Request, ch gen.SalesChannelRow) (bool, createUserEnvelope) {
	t.Helper()
	w := httptest.NewRecorder()
	ok := h.requireGatewaySession(context.Background(), w, req, ch, "cs")
	var env createUserEnvelope
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode envelope: %v; body=%s", err, w.Body.String())
		}
	}
	return ok, env
}

func TestBil24_481_RequireGatewaySession_Valid_ExtendsExpiry(t *testing.T) {
	ch := createUserChannel(t)
	sq := newMemSessionQuerier()
	token, userID, rowID := seedSession(sq, ch, time.Now().UTC().Add(time.Hour))
	h := buildUserHandler(t, ch, sq, newMemCustomerStore())

	ok, env := runSessionGuard(t, h,
		bil24Request{Command: "RESERVATION", FID: "1271", SessionID: token, UserID: userID}, ch)
	if !ok {
		t.Fatalf("valid session rejected: %+v", env)
	}
	newExpiry, extended := sq.extended[rowID]
	if !extended {
		t.Fatal("a passing session must slide expires_at forward (spec §7.3)")
	}
	if !newExpiry.After(time.Now().UTC().Add(DefaultGatewaySessionTTL - time.Minute)) {
		t.Errorf("expires_at extended to %s, want ≈ now+30d", newExpiry)
	}
}

func TestBil24_481_RequireGatewaySession_RejectionsReturnCode1(t *testing.T) {
	ch := createUserChannel(t)
	otherOrgChannel := gen.SalesChannelRow{ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 1271}

	cases := []struct {
		name    string
		channel gen.SalesChannelRow
		// mutate adjusts the seeded fixture / request for this case.
		mutate func(sq *memSessionQuerier, req *bil24Request)
	}{
		{
			name:    "missing_sessionId",
			channel: ch,
			mutate:  func(_ *memSessionQuerier, req *bil24Request) { req.SessionID = "" },
		},
		{
			name:    "blank_sessionId",
			channel: ch,
			mutate:  func(_ *memSessionQuerier, req *bil24Request) { req.SessionID = "   " },
		},
		{
			name:    "missing_userId",
			channel: ch,
			mutate:  func(_ *memSessionQuerier, req *bil24Request) { req.UserID = 0 },
		},
		{
			name:    "unknown_sessionId",
			channel: ch,
			mutate:  func(_ *memSessionQuerier, req *bil24Request) { req.SessionID = "nope" },
		},
		{
			name:    "expired_session",
			channel: ch,
			mutate: func(sq *memSessionQuerier, req *bil24Request) {
				row := sq.byToken[req.SessionID]
				row.ExpiresAt = time.Now().UTC().Add(-time.Minute)
				sq.byToken[req.SessionID] = row
			},
		},
		{
			// Cross-tenant replay: the fid's channel belongs to another org.
			name:    "cross_org_session",
			channel: otherOrgChannel,
			mutate:  func(*memSessionQuerier, *bil24Request) {},
		},
		{
			name:    "unknown_userId",
			channel: ch,
			mutate:  func(_ *memSessionQuerier, req *bil24Request) { req.UserID = 1000009999 },
		},
		{
			name:    "userId_owns_another_session",
			channel: ch,
			mutate: func(sq *memSessionQuerier, req *bil24Request) {
				sq.bySystemID[req.UserID] = gen.CustomerRow{ID: uuid.New(), SystemID: req.UserID}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sq := newMemSessionQuerier()
			token, userID, rowID := seedSession(sq, ch, time.Now().UTC().Add(time.Hour))
			req := bil24Request{Command: "RESERVATION", FID: "1271", SessionID: token, UserID: userID}
			tc.mutate(sq, &req)

			h := buildUserHandler(t, ch, sq, newMemCustomerStore())
			ok, env := runSessionGuard(t, h, req, tc.channel)
			if ok {
				t.Fatalf("%s: guard accepted an invalid session", tc.name)
			}
			if env.ResultCode != ResultCodeSessionExpired {
				t.Errorf("resultCode = %d, want %d (session expired)",
					env.ResultCode, ResultCodeSessionExpired)
			}
			if env.Description == "" {
				t.Error("the code-1 envelope must carry a description")
			}
			if _, extended := sq.extended[rowID]; extended {
				t.Error("a rejected session must not have its expiry refreshed")
			}
		})
	}
}

// A database outage is not a session problem: the site should retry (-1),
// not throw the visitor's cart away by re-running CREATE_USER.
func TestBil24_481_RequireGatewaySession_DBError_ReturnsTransient(t *testing.T) {
	ch := createUserChannel(t)
	sq := newMemSessionQuerier()
	token, userID, _ := seedSession(sq, ch, time.Now().UTC().Add(time.Hour))
	sq.lookupErr = fmt.Errorf("connection reset")
	h := buildUserHandler(t, ch, sq, newMemCustomerStore())

	ok, env := runSessionGuard(t, h,
		bil24Request{Command: "RESERVATION", FID: "1271", SessionID: token, UserID: userID}, ch)
	if ok {
		t.Fatal("guard must fail closed on a database error")
	}
	if env.ResultCode != ResultCodeTransient {
		t.Errorf("resultCode = %d, want %d (transient)", env.ResultCode, ResultCodeTransient)
	}
}

// The guard is a pass-through when the session surface is not wired — the
// nil-dependency convention that keeps the pre-#481 unit suites green.
func TestBil24_481_RequireGatewaySession_NotWired_PassesThrough(t *testing.T) {
	ch := createUserChannel(t)
	h := buildUserHandler(t, ch, nil, newMemCustomerStore())

	ok, env := runSessionGuard(t, h, bil24Request{Command: "RESERVATION", FID: "1271"}, ch)
	if !ok {
		t.Fatalf("unwired guard must pass through; got envelope %+v", env)
	}
}
