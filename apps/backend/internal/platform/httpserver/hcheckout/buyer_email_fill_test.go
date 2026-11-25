package hcheckout

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// sqlRecorderTx is a pgx.Tx that records the SQL it is given, in order. Its
// savepoint is itself. Only what fillMissingBuyerEmailTx calls is implemented.
type sqlRecorderTx struct {
	pgx.Tx
	sql []string
}

func (t *sqlRecorderTx) Begin(context.Context) (pgx.Tx, error) { return t, nil }
func (t *sqlRecorderTx) Commit(context.Context) error          { return nil }
func (t *sqlRecorderTx) Rollback(context.Context) error        { return nil }
func (t *sqlRecorderTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	t.sql = append(t.sql, sql)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (t *sqlRecorderTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	t.sql = append(t.sql, sql)
	return recorderRow{}
}

type recorderRow struct{}

func (recorderRow) Scan(dest ...any) error {
	for _, d := range dest {
		if p, ok := d.(*uuid.UUID); ok {
			*p = uuid.New()
		}
	}
	return nil
}

// The fill takes the checkout row BEFORE it writes the order row: the order
// of the buyer attach, so the two can never deadlock.
func TestFillMissingBuyerEmail_LocksCheckoutBeforeOrder(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	tx := &sqlRecorderTx{}
	h.fillMissingBuyerEmailTx(context.Background(), tx, uuid.New(), "paid@example.com")
	if len(tx.sql) != 2 {
		t.Fatalf("statements = %d (%q), want 2", len(tx.sql), tx.sql)
	}
	if !strings.Contains(tx.sql[0], "FROM checkout_sessions") || !strings.Contains(tx.sql[0], "FOR UPDATE") {
		t.Errorf("first statement does not lock the checkout: %q", tx.sql[0])
	}
	if !strings.Contains(tx.sql[1], "UPDATE orders") {
		t.Errorf("second statement does not fill the order: %q", tx.sql[1])
	}
}

// No address or a malformed one touches nothing.
func TestFillMissingBuyerEmail_InvalidAddressWritesNothing(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, email := range []string{"", "  ", "not-an-address"} {
		tx := &sqlRecorderTx{}
		h.fillMissingBuyerEmailTx(context.Background(), tx, uuid.New(), email)
		if len(tx.sql) != 0 {
			t.Errorf("%q: statements = %q, want none", email, tx.sql)
		}
	}
}

// A prepared widget order (spec 65) may reach payment without its buyer: the
// completed session's collected e-mail must come out of the envelope.
func TestParseWebhook_BuyerEmailFromCheckoutSession(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			"customer_details wins",
			`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","payment_status":"paid",
			  "customer_email":"created@example.com","customer_details":{"email":" paid@example.com "}}}}`,
			"paid@example.com",
		},
		{
			"customer_email when no details",
			`{"id":"evt_2","type":"checkout.session.completed","data":{"object":{"id":"cs_2","payment_status":"paid",
			  "customer_email":"created@example.com","customer_details":null}}}`,
			"created@example.com",
		},
		{
			"empty details email falls back",
			`{"id":"evt_3","type":"checkout.session.completed","data":{"object":{"id":"cs_3","payment_status":"paid",
			  "customer_email":"created@example.com","customer_details":{"email":""}}}}`,
			"created@example.com",
		},
		{
			"none",
			`{"id":"evt_4","type":"checkout.session.completed","data":{"object":{"id":"cs_4","payment_status":"paid"}}}`,
			"",
		},
	}
	for _, tc := range cases {
		req, err := parseWebhookPaymentIntentRequest([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		if req.BuyerEmail != tc.want {
			t.Errorf("%s: BuyerEmail = %q, want %q", tc.name, req.BuyerEmail, tc.want)
		}
	}
}

// The flat mock shape carries no address.
func TestParseWebhook_FlatShapeHasNoBuyerEmail(t *testing.T) {
	req, err := parseWebhookPaymentIntentRequest([]byte(`{"provider_payment_id":"pi_1","event_type":"mock.succeeded","buyer_email":"x@example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.BuyerEmail != "" {
		t.Fatalf("flat shape BuyerEmail = %q, want empty", req.BuyerEmail)
	}
}
