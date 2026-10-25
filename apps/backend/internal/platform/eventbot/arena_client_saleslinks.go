package eventbot

// arena_client_saleslinks.go — the calls behind the bot's "Links and QR"
// screens (spec 43, QL-8). The rules live in arena-api (hsalelinks): the
// target must belong to the organization (422 sales_link.invalid_target), the
// address must be http or https, the label is 1-120 characters, and the short
// address and the page it leads to are worked out there. The bot only shows
// them. An error never carries a path with a query: the stats call holds
// dates only, but the habit stays the same as in the other clients.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// The target kinds, the placement kinds and the statuses as the API spells
// them (plain strings in the OpenAPI document).
const (
	slKindOrgPage      = "org_page"
	slKindPromoterPage = "promoter_page"
	slKindEvent        = "event"
	slKindSession      = "session"
	slKindURL          = "url"

	slStatusActive   = "active"
	slStatusArchived = "archived"
)

// slPlacements are the placement buttons, in the order they are shown.
var slPlacements = []string{"poster", "flyer", "sign", "social", "email", "messenger", "other"}

// slQRMaxBytes bounds the image the bot forwards; a 2048-pixel PNG of a QR
// code is a few tens of kilobytes.
const slQRMaxBytes = 5 << 20

// slInsertPaper is the paper of the PDF insert the bot sends: A6, a flyer
// stand or a table tent. The web event center offers A5 as well.
const slInsertPaper = "a6"

// SalesLinkCreate is the body of a new link. The utm labels are never sent:
// the server fills the defaults of spec 43 §2 item 3.
type SalesLinkCreate struct {
	Target        openapi.SalesLinkTarget
	Label         string
	PlacementKind string
}

// SalesLinkQuery is one page request of the list.
type SalesLinkQuery struct {
	EventID   *uuid.UUID
	SessionID *uuid.UUID
	Status    string // active (default on the server), archived or all
	Limit     int
	Cursor    string
}

// SalesLinkPatch names the keys a PATCH changes; a nil field keeps its stored
// value. The utm key is never sent: a null there would clear the labels.
type SalesLinkPatch struct {
	Label         *string
	PlacementKind *string
	Target        *openapi.SalesLinkTarget
	Status        *string
}

func salesLinksPath(orgID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/sales-links"
}

func salesLinkPath(orgID, linkID uuid.UUID) string {
	return salesLinksPath(orgID) + "/" + linkID.String()
}

// CreateSalesLink creates a link and returns it with its short address.
func (c *ArenaClient) CreateSalesLink(ctx context.Context, jwt string, orgID uuid.UUID, in SalesLinkCreate) (openapi.SalesLink, error) {
	body := map[string]any{
		"target":         in.Target,
		"label":          in.Label,
		"placement_kind": in.PlacementKind,
	}
	var out openapi.SalesLink
	err := c.do(ctx, http.MethodPost, salesLinksPath(orgID), jwt, body, &out)
	return out, err
}

// ListSalesLinks returns a page of the organization's links, newest first.
func (c *ArenaClient) ListSalesLinks(ctx context.Context, jwt string, orgID uuid.UUID, q SalesLinkQuery) (openapi.SalesLinkList, error) {
	v := url.Values{}
	if q.EventID != nil {
		v.Set("event_id", q.EventID.String())
	}
	if q.SessionID != nil {
		v.Set("session_id", q.SessionID.String())
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	path := salesLinksPath(orgID)
	if enc := v.Encode(); enc != "" {
		path += "?" + enc
	}
	var out openapi.SalesLinkList
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// GetSalesLink returns one link; another organization's link is the route's
// own 404 sales_link.not_found.
func (c *ArenaClient) GetSalesLink(ctx context.Context, jwt string, orgID, linkID uuid.UUID) (openapi.SalesLink, error) {
	var out openapi.SalesLink
	err := c.do(ctx, http.MethodGet, salesLinkPath(orgID, linkID), jwt, nil, &out)
	return out, err
}

// SalesLinkStats returns the scans and the sales of one link. Empty from and
// to ask for the server's default range (the last 30 days, UTC days).
func (c *ArenaClient) SalesLinkStats(ctx context.Context, jwt string, orgID, linkID uuid.UUID, from, to string) (openapi.SalesLinkStats, error) {
	path := salesLinkPath(orgID, linkID) + "/stats"
	v := url.Values{}
	if from != "" {
		v.Set("from", from)
	}
	if to != "" {
		v.Set("to", to)
	}
	if enc := v.Encode(); enc != "" {
		path += "?" + enc
	}
	var out openapi.SalesLinkStats
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// UpdateSalesLink changes the keys the patch names. Moving the target of a
// printed code is audited by the server (v1.sales_link.retarget).
func (c *ArenaClient) UpdateSalesLink(ctx context.Context, jwt string, orgID, linkID uuid.UUID, p SalesLinkPatch) (openapi.SalesLink, error) {
	body := map[string]any{}
	if p.Label != nil {
		body["label"] = *p.Label
	}
	if p.PlacementKind != nil {
		body["placement_kind"] = *p.PlacementKind
	}
	if p.Target != nil {
		body["target"] = *p.Target
	}
	if p.Status != nil {
		body["status"] = *p.Status
	}
	var out openapi.SalesLink
	err := c.do(ctx, http.MethodPatch, salesLinkPath(orgID, linkID), jwt, body, &out)
	return out, err
}

// SalesLinkQR downloads the code of a link: format png or svg (size in
// pixels, 256-2048), or pdf, the printable A6 insert (QL-13). The body is
// the file itself.
func (c *ArenaClient) SalesLinkQR(ctx context.Context, jwt string, orgID, linkID uuid.UUID, format string, size int) ([]byte, error) {
	if format != "png" && format != "svg" && format != "pdf" {
		return nil, fmt.Errorf("sales link qr: format %q", format)
	}
	path := salesLinkPath(orgID, linkID) + "/qr?format=" + format
	if format == "pdf" {
		path += "&paper=" + slInsertPaper
	} else if size > 0 {
		path += "&size=" + strconv.Itoa(size)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build GET sales link qr: %w", err)
	}
	switch format {
	case "svg":
		req.Header.Set("Accept", "image/svg+xml")
	case "pdf":
		req.Header.Set("Accept", "application/pdf")
	default:
		req.Header.Set("Accept", "image/png")
	}
	req.Header.Set("X-Admin-Reason", AdminReason)
	req.Header.Set(audit.HeaderClientChannel, ClientChannel)
	req.Header.Set("Authorization", "Bearer "+jwt)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET sales link qr: %w", unwrapURLError(err))
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, slQRMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read sales link qr: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, apiErrorFrom(res.StatusCode, raw)
	}
	if len(raw) > slQRMaxBytes || len(raw) == 0 {
		return nil, fmt.Errorf("sales link qr: %d bytes", len(raw))
	}
	return raw, nil
}
