package hsalelinks

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

// Valid values of the three string enums. In the OpenAPI document they are
// plain strings with the values listed in the description.
var (
	targetKinds = []string{
		salelinks.KindOrgPage, salelinks.KindPromoterPage, salelinks.KindEvent,
		salelinks.KindSession, salelinks.KindURL,
	}
	placementKinds = []string{"poster", "flyer", "sign", "social", "email", "messenger", "other"}
	linkStatuses   = []string{"active", "archived"}
)

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// optional is the tri-state of a PATCH field: Present false = the key was
// absent (keep), Present true with a nil Value = the key was null.
type optional[T any] struct {
	Present bool
	Value   *T
}

func (o *optional[T]) UnmarshalJSON(data []byte) error {
	o.Present = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// targetInput is the target object of a create or patch body.
type targetInput struct {
	Kind       string  `json:"kind"`
	EventID    *string `json:"event_id"`
	SessionID  *string `json:"session_id"`
	PromoterID *string `json:"promoter_id"`
	URL        *string `json:"url"`
}

// createRequest is the body of POST .../sales-links.
type createRequest struct {
	Target        targetInput     `json:"target"`
	Label         string          `json:"label"`
	PlacementKind string          `json:"placement_kind"`
	UTM           json.RawMessage `json:"utm"`
}

// patchRequest is the body of PATCH .../sales-links/{id}.
type patchRequest struct {
	Label         optional[string]          `json:"label"`
	PlacementKind optional[string]          `json:"placement_kind"`
	UTM           optional[json.RawMessage] `json:"utm"`
	Target        optional[targetInput]     `json:"target"`
	Status        optional[string]          `json:"status"`
}

// targetJSON is the target object of a SalesLink answer.
type targetJSON struct {
	Kind       string     `json:"kind"`
	EventID    *uuid.UUID `json:"event_id"`
	SessionID  *uuid.UUID `json:"session_id"`
	PromoterID *uuid.UUID `json:"promoter_id"`
	URL        *string    `json:"url"`
}

// salesLinkJSON is the SalesLink answer.
type salesLinkJSON struct {
	ID                  uuid.UUID         `json:"id"`
	OrgID               uuid.UUID         `json:"org_id"`
	Code                string            `json:"code"`
	ShortURL            string            `json:"short_url"`
	Target              targetJSON        `json:"target"`
	Label               string            `json:"label"`
	PlacementKind       string            `json:"placement_kind"`
	UTM                 map[string]string `json:"utm"`
	Status              string            `json:"status"`
	ScanCount           int64             `json:"scan_count"`
	SuspectScanCount    int64             `json:"suspect_scan_count"`
	LastScannedAt       *string           `json:"last_scanned_at"`
	OrdersCount         int64             `json:"orders_count"`
	DestinationURL      string            `json:"destination_url"`
	DestinationDegraded bool              `json:"destination_degraded"`
	CreatedBy           *uuid.UUID        `json:"created_by"`
	CreatedVia          *string           `json:"created_via"`
	CreatedAt           string            `json:"created_at"`
	UpdatedAt           string            `json:"updated_at"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func linkTarget(row gen.SalesLinkRow) salelinks.Target {
	t := salelinks.Target{
		Kind: row.TargetKind, EventID: row.EventID, SessionID: row.SessionID, PromoterID: row.PromoterID,
	}
	if row.URL != nil {
		t.URL = *row.URL
	}
	return t
}

// factsCache answers the facts lookups of one request once per distinct
// target, so a page of links on one event costs one query, not one per link.
type factsCache struct {
	inner salelinks.FactsQuerier
	rows  map[string]gen.SalesLinkTargetFactsRow
}

func newFactsCache(inner salelinks.FactsQuerier) *factsCache {
	return &factsCache{inner: inner, rows: map[string]gen.SalesLinkTargetFactsRow{}}
}

func idStr(id *uuid.UUID) string {
	if id == nil {
		return "-"
	}
	return id.String()
}

func (c *factsCache) GetSalesLinkTargetFacts(ctx context.Context, orgID uuid.UUID, eventID, sessionID, promoterID *uuid.UUID) (gen.SalesLinkTargetFactsRow, error) {
	key := orgID.String() + "|" + idStr(eventID) + "|" + idStr(sessionID) + "|" + idStr(promoterID)
	if row, ok := c.rows[key]; ok {
		return row, nil
	}
	row, err := c.inner.GetSalesLinkTargetFacts(ctx, orgID, eventID, sessionID, promoterID)
	if err != nil {
		return row, err
	}
	c.rows[key] = row
	return row, nil
}

// present builds the SalesLink answer: the row, its short address and the
// destination the code leads to right now (spec 43 section 4.3). A destination
// that cannot be worked out is logged and left empty (and flagged degraded).
func (h *Handler) present(ctx context.Context, facts salelinks.FactsQuerier, row gen.SalesLinkRow) salesLinkJSON {
	utm := map[string]string{}
	if len(row.UTM) > 0 {
		_ = json.Unmarshal(row.UTM, &utm)
	}
	out := salesLinkJSON{
		ID: row.ID, OrgID: row.OrgID, Code: row.Code,
		ShortURL: salelinks.ShortURL(h.shortBase, row.Code),
		Target: targetJSON{
			Kind: row.TargetKind, EventID: row.EventID, SessionID: row.SessionID,
			PromoterID: row.PromoterID, URL: row.URL,
		},
		Label: row.Label, PlacementKind: row.PlacementKind, UTM: utm, Status: row.Status,
		ScanCount: row.ScanCount, SuspectScanCount: row.SuspectScanCount, OrdersCount: row.OrdersCount,
		CreatedBy: row.CreatedBy, CreatedVia: row.CreatedVia,
		CreatedAt: rfc3339(row.CreatedAt), UpdatedAt: rfc3339(row.UpdatedAt),
	}
	if row.LastScannedAt != nil {
		s := rfc3339(*row.LastScannedAt)
		out.LastScannedAt = &s
	}
	dest, degraded, err := salelinks.Resolve(ctx, facts, h.ticketsBase, salelinks.Link{
		OrgID: row.OrgID, Code: row.Code, Target: linkTarget(row), Archived: row.Status == "archived",
	})
	if err != nil {
		h.logger.Warn("sales links: destination not resolved", "link_id", row.ID.String(), "error", err.Error())
	}
	out.DestinationURL, out.DestinationDegraded = dest, degraded
	return out
}
