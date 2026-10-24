package hbil24

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

func TestGatewayAttribution_ForUpdate(t *testing.T) {
	landed := time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)
	existing := gen.OrderRow{Attribution: json.RawMessage(`{"ref":"aaaaaaa","landed_at":"2026-10-23T10:00:00Z"}`)}

	t.Run("absent keeps the order as it is", func(t *testing.T) {
		link, raw := gatewayAttribution{}.forUpdate(existing)
		if link != nil || raw != nil {
			t.Fatalf("want nothing to write, got %v %s", link, raw)
		}
	})

	t.Run("a new touch wins and the earlier one becomes first", func(t *testing.T) {
		id := uuid.New()
		g := gatewayAttribution{present: true, linkID: &id,
			attr: attribution.Attribution{Touch: attribution.Touch{Ref: "bbbbbbb", LandedAt: landed}}}
		link, raw := g.forUpdate(existing)
		if link == nil || *link != id {
			t.Fatalf("link = %v, want %s", link, id)
		}
		want := `{"ref":"bbbbbbb","landed_at":"2026-10-24T18:00:00Z","first":{"ref":"aaaaaaa","landed_at":"2026-10-23T10:00:00Z"}}`
		if string(raw) != want {
			t.Fatalf("attribution = %s, want %s", raw, want)
		}
	})

	t.Run("a broken stored value is replaced", func(t *testing.T) {
		g := gatewayAttribution{present: true,
			attr: attribution.Attribution{Touch: attribution.Touch{UTMSource: "qr", LandedAt: landed}}}
		_, raw := g.forUpdate(gen.OrderRow{Attribution: json.RawMessage(`[1]`)})
		if string(raw) != `{"utm_source":"qr","landed_at":"2026-10-24T18:00:00Z"}` {
			t.Fatalf("attribution = %s", raw)
		}
	})
}
