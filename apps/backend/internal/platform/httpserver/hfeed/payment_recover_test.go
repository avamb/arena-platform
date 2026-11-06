package hfeed

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

type fakeRecoverStore struct {
	rows       []gen.StalledHostedPaymentAttemptRow
	asked      []string
	before     time.Time
	storedURLs map[uuid.UUID]string
}

func (s *fakeRecoverStore) ListStalledHostedPaymentAttempts(_ context.Context, provider string, before time.Time, _ int32) ([]gen.StalledHostedPaymentAttemptRow, error) {
	s.asked = append(s.asked, provider)
	s.before = before
	return s.rows, nil
}

func (s *fakeRecoverStore) SetPaymentIntentHostedURL(_ context.Context, id uuid.UUID, url string) (bool, error) {
	if s.storedURLs == nil {
		s.storedURLs = map[uuid.UUID]string{}
	}
	s.storedURLs[id] = url
	return true, nil
}

type fakeRecoverProvider struct {
	lookups   map[string]payments.HostedPaymentLookupResult
	lookupErr map[string]error
	configID  uuid.UUID
	starts    []HostedCheckoutRequest
	answerID  string // overrides the id the page is filed under
}

func (p *fakeRecoverProvider) LookupHostedPayment(_ context.Context, _ uuid.UUID, provider, ref string) (HostedPaymentLookup, error) {
	if err := p.lookupErr[ref]; err != nil {
		return HostedPaymentLookup{}, err
	}
	return HostedPaymentLookup{Result: p.lookups[ref], Provider: provider, ConfigID: p.configID}, nil
}

func (p *fakeRecoverProvider) StartHostedCheckout(_ context.Context, req HostedCheckoutRequest) (*HostedCheckoutResult, error) {
	p.starts = append(p.starts, req)
	id := req.reference()
	if p.answerID != "" {
		id = p.answerID
	}
	return &HostedCheckoutResult{RedirectURL: "https://pay.test/" + id, SessionID: id, Provider: "allpay"}, nil
}

func stalledRow(holdExpires time.Time) gen.StalledHostedPaymentAttemptRow {
	cs := uuid.New()
	ret := "https://tickets.test/shows/x"
	email := "buyer@example.test"
	return gen.StalledHostedPaymentAttemptRow{
		PaymentIntentID:   uuid.New(),
		OrgID:             uuid.New(),
		Provider:          "allpay",
		ProviderPaymentID: cs.String(),
		Amount:            12050,
		Currency:          "ILS",
		CheckoutSessionID: cs,
		ChannelID:         uuid.New(),
		CheckoutToken:     "tok-" + cs.String()[:8],
		ReturnURL:         &ret,
		OrderID:           uuid.New(),
		BuyerEmail:        &email,
		HoldExpiresAt:     holdExpires,
		EventName:         "Wine evening",
	}
}

func newTestRecoverer(store *fakeRecoverStore, prov *fakeRecoverProvider, apply ConfirmedPaymentApplier, now time.Time) *PaymentRecoverer {
	return &PaymentRecoverer{
		Store:      store,
		Provider:   prov,
		Apply:      apply,
		ReturnURLs: ReturnURLPolicy{AllowedOrigins: []string{"https://tickets.test"}},
		Grace:      2 * time.Minute,
		Providers:  []string{"allpay"},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        func() time.Time { return now },
	}
}

func TestPaymentRecover_UnpaidAsksForThePageAgainUnderTheSameReference(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	row := stalledRow(now.Add(30 * time.Minute))
	store := &fakeRecoverStore{rows: []gen.StalledHostedPaymentAttemptRow{row}}
	prov := &fakeRecoverProvider{lookups: map[string]payments.HostedPaymentLookupResult{
		row.ProviderPaymentID: {State: payments.HostedPaymentUnpaid},
	}}
	applied := 0
	r := newTestRecoverer(store, prov, func(context.Context, hcheckout.ConfirmedPaymentSource, payments.NormalizedEvent) error {
		applied++
		return nil
	}, now)

	st, err := r.RecoverStalledPayments(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.Relinked != 1 || applied != 0 {
		t.Fatalf("stats %+v applied %d; want one page asked again, nothing applied", st, applied)
	}
	if got := store.before; !got.Equal(now.Add(-DefaultRecoverMinAge)) {
		t.Fatalf("listed attempts created before %v; want now minus the minimum age", got)
	}
	req := prov.starts[0]
	if req.CheckoutSessionID != row.CheckoutSessionID || req.reference() != row.ProviderPaymentID {
		t.Fatalf("page asked for %s; want the recorded reference %s", req.reference(), row.ProviderPaymentID)
	}
	if req.ExpectProvider != "allpay" || req.RecordAttempt != nil {
		t.Fatalf("request = %+v; want the provider pinned and no second attempt row", req)
	}
	if want := row.HoldExpiresAt.Add(-2 * time.Minute).Unix(); req.ExpiresAtUnix != want {
		t.Fatalf("page expires %d; want the hold minus the grace %d", req.ExpiresAtUnix, want)
	}
	if req.Amount != row.Amount || req.Currency != "ILS" || req.ProductName != "Wine evening" || req.ReturnURL != "https://tickets.test/shows/x" {
		t.Fatalf("request = %+v; want the recorded money, event and return page", req)
	}
	if got := store.storedURLs[row.PaymentIntentID]; got != "https://pay.test/"+row.ProviderPaymentID {
		t.Fatalf("stored page %q", got)
	}
}

func TestPaymentRecover_PaidGoesThroughTheWebhookPath(t *testing.T) {
	now := time.Now()
	row := stalledRow(now.Add(30 * time.Minute))
	store := &fakeRecoverStore{rows: []gen.StalledHostedPaymentAttemptRow{row}}
	ev := payments.NormalizedEvent{Kind: payments.EventPaymentSucceeded, Type: "allpay.payment.paid", ProviderPaymentID: row.ProviderPaymentID}
	prov := &fakeRecoverProvider{
		configID: uuid.New(),
		lookups:  map[string]payments.HostedPaymentLookupResult{row.ProviderPaymentID: {State: payments.HostedPaymentSettled, Event: ev}},
	}
	var gotSrc hcheckout.ConfirmedPaymentSource
	var gotEv payments.NormalizedEvent
	r := newTestRecoverer(store, prov, func(_ context.Context, src hcheckout.ConfirmedPaymentSource, e payments.NormalizedEvent) error {
		gotSrc, gotEv = src, e
		return nil
	}, now)

	st, err := r.RecoverStalledPayments(context.Background(), 10)
	if err != nil || st.Applied != 1 || len(prov.starts) != 0 {
		t.Fatalf("stats %+v err %v starts %d; want the payment applied and no new page", st, err, len(prov.starts))
	}
	if gotSrc.OrgID != row.OrgID || gotSrc.ConfigID != prov.configID || gotSrc.Provider != "allpay" || gotEv.ProviderPaymentID != row.ProviderPaymentID {
		t.Fatalf("applied %+v / %+v", gotSrc, gotEv)
	}
}

func TestPaymentRecover_LeavesWhatItCannotSettle(t *testing.T) {
	now := time.Now()
	tooLate := stalledRow(now.Add(3 * time.Minute)) // page would live 1 minute
	pending := stalledRow(now.Add(30 * time.Minute))
	broken := stalledRow(now.Add(30 * time.Minute))
	store := &fakeRecoverStore{rows: []gen.StalledHostedPaymentAttemptRow{broken, tooLate, pending}}
	prov := &fakeRecoverProvider{
		lookups: map[string]payments.HostedPaymentLookupResult{
			tooLate.ProviderPaymentID: {State: payments.HostedPaymentUnknown},
			pending.ProviderPaymentID: {State: payments.HostedPaymentPending},
		},
		lookupErr: map[string]error{broken.ProviderPaymentID: errors.New("allpay down")},
	}
	r := newTestRecoverer(store, prov, nil, now)

	st, err := r.RecoverStalledPayments(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.Examined != 3 || st.Failed != 1 || st.TooLate != 1 || st.Pending != 1 || len(prov.starts) != 0 {
		t.Fatalf("stats %+v starts %d; want one failure, one too late, one pending, no page", st, len(prov.starts))
	}
}

func TestPaymentRecover_PageUnderAnotherIDIsNotStored(t *testing.T) {
	now := time.Now()
	row := stalledRow(now.Add(30 * time.Minute))
	store := &fakeRecoverStore{rows: []gen.StalledHostedPaymentAttemptRow{row}}
	prov := &fakeRecoverProvider{
		lookups:  map[string]payments.HostedPaymentLookupResult{row.ProviderPaymentID: {State: payments.HostedPaymentUnknown}},
		answerID: "someone-else",
	}
	st, _ := newTestRecoverer(store, prov, nil, now).RecoverStalledPayments(context.Background(), 10)
	if st.Failed != 1 || len(store.storedURLs) != 0 {
		t.Fatalf("stats %+v stored %v; a page filed under another id must not be stored", st, store.storedURLs)
	}
}

func TestPaymentRecover_AllPayIsRecoverable(t *testing.T) {
	found := false
	for _, name := range RecoverableProviders() {
		if name == "allpay" {
			found = true
		}
		if name == "stripe" {
			t.Fatalf("stripe ids are only known from its answer; it must not be recovered by reference")
		}
	}
	if !found {
		t.Fatalf("allpay must be recoverable by reference: %v", RecoverableProviders())
	}
}
