package paymentreceipts

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

type fakeQueries struct {
	row     gen.PaymentIntentReceiptRow
	err     error
	setURL  *string
	setNum  *string
	setID   *uuid.UUID
	setCall int
	// saved is the copy an earlier run already saved (uuid.Nil = none).
	saved uuid.UUID
}

func (f *fakeQueries) GetPaymentReceiptMediaID(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, error) {
	if f.saved == uuid.Nil {
		return uuid.Nil, pgx.ErrNoRows
	}
	return f.saved, nil
}

func (f *fakeQueries) GetPaymentIntentReceipt(context.Context, uuid.UUID) (gen.PaymentIntentReceiptRow, error) {
	return f.row, f.err
}

func (f *fakeQueries) SetPaymentIntentReceipt(_ context.Context, _ uuid.UUID, url, number *string, mediaID *uuid.UUID) error {
	f.setCall++
	f.setURL, f.setNum, f.setID = url, number, mediaID
	return nil
}

type fakeModule struct {
	rec payments.Receipt
	err error
	ref string
}

func (m *fakeModule) Descriptor() payments.Descriptor { return payments.Descriptor{Name: "fake"} }
func (m *fakeModule) FetchReceipt(_ context.Context, ref string) (payments.Receipt, error) {
	m.ref = ref
	return m.rec, m.err
}

type fakeSource struct {
	m      payments.Module
	config *uuid.UUID
}

func (s fakeSource) Build(ctx context.Context, _ uuid.UUID, _ string) (payments.Module, error) {
	if s.config != nil {
		*s.config, _ = refunds.ProviderConfigFromContext(ctx)
	}
	return s.m, nil
}

type fakeStore struct {
	ct    string
	body  []byte
	owner uuid.UUID
	id    uuid.UUID
	calls int
}

func (s *fakeStore) SaveReceipt(_ context.Context, _, pi uuid.UUID, ct string, body []byte) (uuid.UUID, error) {
	s.calls++
	s.ct, s.body, s.owner = ct, body, pi
	return s.id, nil
}

func job(t *testing.T, id uuid.UUID) []byte {
	t.Helper()
	b, err := json.Marshal(Payload{PaymentIntentID: id.String()})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ptr(s string) *string { return &s }

func TestHandler_StoresLinkNumberAndCopy(t *testing.T) {
	id := uuid.New()
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{ID: id, OrgID: uuid.New(), Provider: "stripe", State: "succeeded",
		ProviderPaymentID: ptr("cs_1"), ProviderChargeRef: ptr("pi_1")}}
	m := &fakeModule{rec: payments.Receipt{URL: "https://pay.stripe.com/receipts/x", Number: "1234", File: []byte("%PDF-1"), ContentType: "application/pdf"}}
	st := &fakeStore{id: uuid.New()}
	h := NewHandler(Options{Queries: q, Modules: fakeSource{m: m}, Store: st})
	if err := h(t.Context(), job(t, id)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if m.ref != "pi_1" {
		t.Errorf("fetched %q; want the charge ref", m.ref)
	}
	if st.ct != "application/pdf" || st.owner != id {
		t.Errorf("store got %q owner %s", st.ct, st.owner)
	}
	if q.setCall != 1 || *q.setURL != "https://pay.stripe.com/receipts/x" || *q.setNum != "1234" || *q.setID != st.id {
		t.Errorf("stored url=%v number=%v media=%v", q.setURL, q.setNum, q.setID)
	}
}

func TestHandler_WithoutStoreKeepsTheLinkOnly(t *testing.T) {
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{ID: uuid.New(), Provider: "stripe", State: "succeeded", ProviderPaymentID: ptr("pi_9")}}
	m := &fakeModule{rec: payments.Receipt{URL: "https://pay.stripe.com/receipts/y", File: []byte("<html>")}}
	if err := NewHandler(Options{Queries: q, Modules: fakeSource{m: m}})(t.Context(), job(t, q.row.ID)); err != nil {
		t.Fatal(err)
	}
	if m.ref != "pi_9" || q.setID != nil || q.setURL == nil || q.setNum != nil {
		t.Errorf("ref=%q url=%v number=%v media=%v", m.ref, q.setURL, q.setNum, q.setID)
	}
}

func TestHandler_SkipsWhatIsNotToArchive(t *testing.T) {
	now := time.Now()
	for name, row := range map[string]gen.PaymentIntentReceiptRow{
		"not paid":        {State: "failed", ProviderPaymentID: ptr("pi_1")},
		"already kept":    {State: "succeeded", ProviderPaymentID: ptr("pi_1"), ReceiptArchivedAt: &now},
		"no provider ref": {State: "succeeded"},
	} {
		q := &fakeQueries{row: row}
		m := &fakeModule{}
		if err := NewHandler(Options{Queries: q, Modules: fakeSource{m: m}})(t.Context(), job(t, uuid.New())); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.ref != "" || q.setCall != 0 {
			t.Errorf("%s: the receipt was fetched or stored", name)
		}
	}
	q := &fakeQueries{err: pgx.ErrNoRows}
	if err := NewHandler(Options{Queries: q, Modules: fakeSource{m: &fakeModule{}}})(t.Context(), job(t, uuid.New())); err != nil {
		t.Fatalf("unknown payment intent: %v", err)
	}
}

func TestHandler_NotReadyIsRetried(t *testing.T) {
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{State: "succeeded", ProviderPaymentID: ptr("pi_1")}}
	m := &fakeModule{err: payments.ErrReceiptNotReady}
	err := NewHandler(Options{Queries: q, Modules: fakeSource{m: m}})(t.Context(), job(t, uuid.New()))
	if !errors.Is(err, payments.ErrReceiptNotReady) || q.setCall != 0 {
		t.Fatalf("err = %v, stored %d times; want a retry and nothing stored", err, q.setCall)
	}
}

func TestArchivable_FollowsTheCapability(t *testing.T) {
	for provider, want := range map[string]bool{"stripe": true, "allpay": false, "payplus": false, "flitt": false, "manual": false, "": false} {
		if got := Archivable(provider); got != want {
			t.Errorf("Archivable(%q) = %v; want %v", provider, got, want)
		}
	}
}

func TestHandler_FailedDownloadKeepsTheLinkAndRetries(t *testing.T) {
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{ID: uuid.New(), Provider: "stripe", State: "succeeded", ProviderPaymentID: ptr("pi_1")}}
	m := &fakeModule{rec: payments.Receipt{URL: "https://pay.stripe.com/receipts/z", Number: "9"}, err: payments.ErrReceiptDownload}
	st := &fakeStore{id: uuid.New()}
	err := NewHandler(Options{Queries: q, Modules: fakeSource{m: m}, Store: st})(t.Context(), job(t, q.row.ID))
	if !errors.Is(err, payments.ErrReceiptDownload) {
		t.Fatalf("err = %v; want a retry", err)
	}
	if q.setCall != 1 || q.setURL == nil || *q.setNum != "9" || q.setID != nil || st.calls != 0 {
		t.Fatalf("url=%v number=%v media=%v saves=%d; want the link kept and no copy", q.setURL, q.setNum, q.setID, st.calls)
	}
	// Without a media store there is no copy to wait for.
	q2 := &fakeQueries{row: q.row}
	if err := NewHandler(Options{Queries: q2, Modules: fakeSource{m: m}})(t.Context(), job(t, q.row.ID)); err != nil || q2.setCall != 1 {
		t.Fatalf("no store: err = %v, stored %d times", err, q2.setCall)
	}
}

func TestHandler_UnknownPaymentAtTheProviderIsFinal(t *testing.T) {
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{ID: uuid.New(), Provider: "stripe", State: "succeeded", ProviderPaymentID: ptr("pi_1")}}
	m := &fakeModule{err: payments.ErrReceiptNotFound}
	if err := NewHandler(Options{Queries: q, Modules: fakeSource{m: m}})(t.Context(), job(t, q.row.ID)); err != nil || q.setCall != 0 {
		t.Fatalf("err = %v, stored %d times; want a final no-op", err, q.setCall)
	}
}

func TestHandler_RetryReusesTheSavedCopy(t *testing.T) {
	saved := uuid.New()
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{ID: uuid.New(), OrgID: uuid.New(), Provider: "stripe", State: "succeeded", ProviderPaymentID: ptr("pi_1")}, saved: saved}
	m := &fakeModule{rec: payments.Receipt{URL: "https://pay.stripe.com/receipts/x", File: []byte("%PDF-1"), ContentType: "application/pdf"}}
	st := &fakeStore{id: uuid.New()}
	if err := NewHandler(Options{Queries: q, Modules: fakeSource{m: m}, Store: st})(t.Context(), job(t, q.row.ID)); err != nil {
		t.Fatal(err)
	}
	if st.calls != 0 || q.setID == nil || *q.setID != saved {
		t.Fatalf("saves=%d media=%v; want the earlier copy %s reused", st.calls, q.setID, saved)
	}
}

func TestHandler_ReadsThroughTheConfigThatVerifiedThePayment(t *testing.T) {
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{ID: uuid.New(), Provider: "stripe", State: "succeeded", ProviderPaymentID: ptr("pi_1")}}
	m := &fakeModule{rec: payments.Receipt{URL: "https://pay.stripe.com/receipts/x"}}
	configID := uuid.New()
	var seen uuid.UUID
	raw, err := json.Marshal(Payload{PaymentIntentID: q.row.ID.String(), ConfigID: configID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewHandler(Options{Queries: q, Modules: fakeSource{m: m, config: &seen}})(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if seen != configID {
		t.Fatalf("module built with config %s; want %s", seen, configID)
	}
}
