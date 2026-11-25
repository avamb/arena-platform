package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
)

// trackFakeDB answers the three reads of queueAttachedInitiateCheckout.
type trackFakeDB struct {
	granted    *bool
	buyerCtx   []byte
	sessionID  uuid.UUID
	groups     []orderItemGroup
	noContext  bool
	readsTotal int
}

type trackRow struct {
	scan func(dest ...any) error
}

func (r trackRow) Scan(dest ...any) error { return r.scan(dest...) }

type trackRows struct {
	pgx.Rows
	groups []orderItemGroup
	i      int
}

func (r *trackRows) Next() bool { r.i++; return r.i <= len(r.groups) }
func (r *trackRows) Close()     {}
func (r *trackRows) Err() error { return nil }
func (r *trackRows) Scan(dest ...any) error {
	g := r.groups[r.i-1]
	*dest[0].(*string) = g.TierID
	*dest[1].(*int64) = g.UnitPrice
	*dest[2].(*int) = int(g.Quantity)
	return nil
}

func (d *trackFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected exec")
}

func (d *trackFakeDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	d.readsTotal++
	return &trackRows{groups: d.groups}, nil
}

func (d *trackFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	d.readsTotal++
	switch {
	case strings.Contains(sql, "tracking_consent_granted"):
		return trackRow{func(dest ...any) error {
			*dest[0].(**bool) = d.granted
			return nil
		}}
	case strings.Contains(sql, "checkout_buyer_contexts"):
		return trackRow{func(dest ...any) error {
			if d.noContext {
				return pgx.ErrNoRows
			}
			*dest[0].(*[]byte) = d.buyerCtx
			return nil
		}}
	case strings.Contains(sql, "FROM   orders") || strings.Contains(sql, "FROM orders"):
		return trackRow{func(dest ...any) error {
			*dest[0].(*uuid.UUID) = d.sessionID
			return nil
		}}
	}
	return trackRow{func(...any) error { return errors.New("unexpected query") }}
}

func trackHandler(db *trackFakeDB, f *fakeCartConversions) *Handler {
	return &Handler{logger: slog.Default(), cartConversions: f, checkoutQueries: gen.New(db)}
}

func trackPC() gen.CheckoutPaymentContextRow {
	total, cur := int64(4100), "EUR"
	return gen.CheckoutPaymentContextRow{OrgID: uuid.New(), OrderID: uuid.New(), Total: &total, Currency: &cur}
}

func boolPtr(b bool) *bool { return &b }

func TestQueueAttachedInitiateCheckout(t *testing.T) {
	cleaned := conversions.CleanBuyerContext([]byte(ctxYes), "203.0.113.5", "UA/1")
	groups := []orderItemGroup{{TierID: "a", UnitPrice: 2000, Quantity: 2}}
	cases := []struct {
		name string
		db   *trackFakeDB
		want int
	}{
		{"consent and context", &trackFakeDB{granted: boolPtr(true), buyerCtx: cleaned, groups: groups}, 1},
		{"consent refused", &trackFakeDB{granted: boolPtr(false), buyerCtx: cleaned, groups: groups}, 0},
		{"no stored answer", &trackFakeDB{granted: nil, buyerCtx: cleaned, groups: groups}, 0},
		{"no buyer context row", &trackFakeDB{granted: boolPtr(true), noContext: true, groups: groups}, 0},
		{"empty buyer context", &trackFakeDB{granted: boolPtr(true), buyerCtx: []byte{}, groups: groups}, 0},
	}
	for _, c := range cases {
		f := &fakeCartConversions{}
		c.db.sessionID = uuid.New()
		h := trackHandler(c.db, f)
		cs := uuid.New()
		h.queueAttachedInitiateCheckout(context.Background(), cs, trackPC())
		if len(f.queued) != c.want {
			t.Fatalf("%s: queued %d, want %d", c.name, len(f.queued), c.want)
		}
		if c.want == 0 {
			continue
		}
		ev := f.queued[0]
		if ev.Kind != conversions.KindInitiateCheckout || ev.EventID != "ic:"+cs.String() || ev.Value != 41 ||
			ev.Context.ClientIP != "203.0.113.5" {
			t.Fatalf("%s: event %+v", c.name, ev)
		}
		// A second attach of the same checkout is deduplicated by the id.
		h.queueAttachedInitiateCheckout(context.Background(), cs, trackPC())
		if len(f.queued) != 1 {
			t.Fatalf("%s: repeat queued again", c.name)
		}
	}
}

func TestQueueAttachedInitiateCheckout_NilQueuerIsQuiet(_ *testing.T) {
	h := &Handler{logger: slog.Default()}
	h.queueAttachedInitiateCheckout(context.Background(), uuid.New(), trackPC())
}

func TestBreakdownFromItems(t *testing.T) {
	bd := breakdownFromItems(6150, "EUR", []orderItemGroup{
		{TierID: "a", UnitPrice: 2000, Quantity: 2},
		{TierID: "b", UnitPrice: 2050, Quantity: 1},
	})
	if bd.Quantity != 3 || bd.Subtotal != 6050 || bd.Total != 6150 || bd.Currency != "EUR" || len(bd.Lines) != 2 ||
		bd.Lines[1].UnitPrice != 2050 {
		t.Fatalf("breakdown: %+v", bd)
	}
}
