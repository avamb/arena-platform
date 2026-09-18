//go:build integration

// subscriber_missing_warn_integration_test.go — bug B-1 part 3 regression
// test: when dispatchOrderPaid / dispatchComplimentaryPaid /
// dispatchTicketRefunded find no active MACS subscriber for the event's
// org, the Dispatcher used to return nil silently — a scanner feed going
// dark for an org left no trace in the logs. Dispatcher now logs WARN
// "macs.subscriber_missing" with org_id, event_type and the order/ticket id
// through an injectable *slog.Logger (macs.WithLogger), while still
// returning nil so the outbox does not retry forever.
//
// Run with:
//
//	go test -tags integration -run TestMACS_SubscriberMissingWarn ./apps/backend/internal/platform/macs/
package macs_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// capturingHandler is a minimal slog.Handler that records every log record
// it receives, so the test can assert on the WARN attributes without
// parsing formatted text output.
type capturingHandler struct {
	records *[]slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r)
	return nil
}
func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }

func recordAttr(r slog.Record, key string) (string, bool) {
	var val string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value.String()
			found = true
			return false
		}
		return true
	})
	return val, found
}

// TestMACS_SubscriberMissingWarn_OrderPaid drives dispatchOrderPaid for an
// org that has NO row in macs_webhook_subscribers. resolveOrgID is satisfied
// straight from the event payload's own org_id, so this needs only an
// organizations row — no venue/event/session/order fixture — because
// getMACSSubscriber fails (and the warn fires) before anything else is
// queried.
func TestMACS_SubscriberMissingWarn_OrderPaid(t *testing.T) {
	pool := roundtripPool(t)
	ctx := context.Background()

	orgID := uuid.New()
	orderID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "MACS Subscriber Missing Org", "macs-sub-missing-"+orgID.String()[:8]); err != nil {
		t.Fatalf("seed organizations: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	var records []slog.Record
	logger := slog.New(&capturingHandler{records: &records})
	disp := macs.NewDispatcher(pool, macs.WithLogger(logger))

	err := disp.Dispatch(ctx, outbox.Event{
		EventType:  macs.EventOrderPaid,
		OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"order_id": orderID.String(),
			"org_id":   orgID.String(),
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: got error %v, want nil (missing subscriber must not be retried)", err)
	}

	var warn *slog.Record
	for i := range records {
		if records[i].Message == "macs.subscriber_missing" {
			warn = &records[i]
			break
		}
	}
	if warn == nil {
		t.Fatalf("no macs.subscriber_missing log record found among %d records", len(records))
	}
	if warn.Level != slog.LevelWarn {
		t.Errorf("macs.subscriber_missing level = %v, want WARN", warn.Level)
	}
	if got, ok := recordAttr(*warn, "org_id"); !ok || got != orgID.String() {
		t.Errorf("macs.subscriber_missing org_id = %q (found=%v), want %q", got, ok, orgID.String())
	}
	if got, ok := recordAttr(*warn, "event_type"); !ok || got != macs.EventOrderPaid {
		t.Errorf("macs.subscriber_missing event_type = %q (found=%v), want %q", got, ok, macs.EventOrderPaid)
	}
	if got, ok := recordAttr(*warn, "order_id"); !ok || got != orderID.String() {
		t.Errorf("macs.subscriber_missing order_id = %q (found=%v), want %q", got, ok, orderID.String())
	}
}
