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
)

type fakeQueries struct {
	row     gen.PaymentIntentReceiptRow
	err     error
	setURL  *string
	setNum  *string
	setID   *uuid.UUID
	setCall int
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

type fakeSource struct{ m payments.Module }

func (s fakeSource) Build(context.Context, uuid.UUID, string) (payments.Module, error) {
	return s.m, nil
}

type fakeStore struct {
	ct    string
	body  []byte
	owner uuid.UUID
	id    uuid.UUID
}

func (s *fakeStore) SaveReceipt(_ context.Context, _, pi uuid.UUID, ct string, body []byte) (uuid.UUID, error) {
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
	h := NewHandler(Options{Queries: q, Modules: fakeSource{m}, Store: st})
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
	if err := NewHandler(Options{Queries: q, Modules: fakeSource{m}})(t.Context(), job(t, q.row.ID)); err != nil {
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
		if err := NewHandler(Options{Queries: q, Modules: fakeSource{m}})(t.Context(), job(t, uuid.New())); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.ref != "" || q.setCall != 0 {
			t.Errorf("%s: the receipt was fetched or stored", name)
		}
	}
	q := &fakeQueries{err: pgx.ErrNoRows}
	if err := NewHandler(Options{Queries: q, Modules: fakeSource{&fakeModule{}}})(t.Context(), job(t, uuid.New())); err != nil {
		t.Fatalf("unknown payment intent: %v", err)
	}
}

func TestHandler_NotReadyIsRetried(t *testing.T) {
	q := &fakeQueries{row: gen.PaymentIntentReceiptRow{State: "succeeded", ProviderPaymentID: ptr("pi_1")}}
	m := &fakeModule{err: payments.ErrReceiptNotReady}
	err := NewHandler(Options{Queries: q, Modules: fakeSource{m}})(t.Context(), job(t, uuid.New()))
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
