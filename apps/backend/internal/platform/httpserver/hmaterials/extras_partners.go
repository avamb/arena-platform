package hmaterials

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// PartnerLink is the JSON shape of one "where else to buy" link.
type PartnerLink struct {
	ID         string `json:"id"`
	EventID    string `json:"event_id"`
	URL        string `json:"url"`
	SellerName string `json:"seller_name"`
	Position   int    `json:"position"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func partnerLinkJSON(r gen.EventPartnerLinkRow) PartnerLink {
	return PartnerLink{
		ID: r.ID.String(), EventID: r.EventID.String(), URL: r.URL, SellerName: r.SellerName,
		Position: int(r.Position), CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt),
	}
}

func partnerLinksJSON(rows []gen.EventPartnerLinkRow) []PartnerLink {
	out := make([]PartnerLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, partnerLinkJSON(r))
	}
	return out
}

func invalidPartnerURL(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_partner_url",
		"the link must be a full https address such as https://tickets.example.com/event, without spaces",
		r, map[string]any{"field": "url"},
	))
}

func invalidSellerName(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_seller_name",
		"the seller name must be 1 to 120 characters without line breaks",
		r, map[string]any{"field": "seller_name"},
	))
}

func partnerNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"event.partner_link_not_found", "partner link not found", r,
	))
}

// HandleListPartnerLinks serves GET .../partner-links.
func (h *ExtrasHandler) HandleListPartnerLinks(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	rows, err := h.queries.ListEventPartnerLinks(r.Context(), eventID)
	if err != nil {
		h.failed(w, r, "list partner links", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": partnerLinksJSON(rows), "max_items": eventmaterials.MaxPartnerLinks})
}

type createPartnerRequest struct {
	URL        string  `json:"url"`
	SellerName *string `json:"seller_name"`
}

// HandleCreatePartnerLink serves POST .../partner-links. The seller name is
// the host of the address unless the client sends its own.
func (h *ExtrasHandler) HandleCreatePartnerLink(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	var req createPartnerRequest
	if !readBody(w, r, &req) {
		return
	}
	link, ok := NormalizePartnerURL(req.URL)
	if !ok {
		invalidPartnerURL(w, r)
		return
	}
	name := SellerNameFromURL(link)
	if req.SellerName != nil && strings.TrimSpace(*req.SellerName) != "" {
		if name, ok = NormalizeSellerName(*req.SellerName); !ok {
			invalidSellerName(w, r)
			return
		}
	}
	if name == "" {
		invalidPartnerURL(w, r)
		return
	}

	ctx := r.Context()
	var row gen.EventPartnerLinkRow
	err := h.inTx(ctx, eventID, partnerLinksLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		var err error
		row, err = q.InsertEventPartnerLink(ctx, orgID, eventID, link, name, eventmaterials.MaxPartnerLinks)
		if err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.partner_link.create", eventID, map[string]any{
			"link_id": row.ID.String(), "position": row.Position,
		})
	})
	switch {
	case err == nil:
		httputil.WriteJSON(w, http.StatusCreated, partnerLinkJSON(row))
	case errors.Is(err, pgx.ErrNoRows):
		partnerLinksFull(w, r, "an event can show up to 3 links to other sellers; please remove one before adding another")
	default:
		if _, isUnique := uniqueViolation(err); isUnique {
			// Lost a race for a position despite the lock (another writer path).
			partnerLinksFull(w, r, "the links changed, reload them and try again")
			return
		}
		h.failed(w, r, "create partner link", err)
	}
}

func partnerLinksFull(w http.ResponseWriter, r *http.Request, msg string) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		"event.partner_links_full", msg, r, map[string]any{"max_items": eventmaterials.MaxPartnerLinks},
	))
}

type patchPartnerRequest struct {
	URL        *string `json:"url"`
	SellerName *string `json:"seller_name"`
}

// HandlePatchPartnerLink serves PATCH .../partner-links/{link_id}. An absent
// member keeps the stored value. When only the address changes, a seller name
// that was derived from the old address follows it; a name the client typed
// is kept.
func (h *ExtrasHandler) HandlePatchPartnerLink(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	linkID, ok := pathID(w, r, "link_id", "event.partner_link_not_found", "partner link")
	if !ok {
		return
	}
	var req patchPartnerRequest
	if !readBody(w, r, &req) {
		return
	}
	if req.URL == nil && req.SellerName == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"event.invalid_body", "send url, seller_name or both", r,
		))
		return
	}
	ctx := r.Context()
	cur, err := h.queries.GetEventPartnerLink(ctx, linkID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			partnerNotFound(w, r)
			return
		}
		h.failed(w, r, "read partner link", err)
		return
	}
	link, name := cur.URL, cur.SellerName
	if req.URL != nil {
		if link, ok = NormalizePartnerURL(*req.URL); !ok {
			invalidPartnerURL(w, r)
			return
		}
		if name == SellerNameFromURL(cur.URL) {
			name = SellerNameFromURL(link)
		}
	}
	if req.SellerName != nil && strings.TrimSpace(*req.SellerName) != "" {
		if name, ok = NormalizeSellerName(*req.SellerName); !ok {
			invalidSellerName(w, r)
			return
		}
	}
	if name == "" {
		invalidPartnerURL(w, r)
		return
	}
	var row gen.EventPartnerLinkRow
	err = h.inTx(ctx, eventID, partnerLinksLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		var err error
		if row, err = q.UpdateEventPartnerLink(ctx, linkID, eventID, link, name); err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.partner_link.update", eventID, map[string]any{"link_id": linkID.String()})
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			partnerNotFound(w, r)
			return
		}
		h.failed(w, r, "update partner link", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, partnerLinkJSON(row))
}

// HandleDeletePartnerLink serves DELETE .../partner-links/{link_id}; the
// links behind it move up so positions stay 0..n-1.
func (h *ExtrasHandler) HandleDeletePartnerLink(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	linkID, ok := pathID(w, r, "link_id", "event.partner_link_not_found", "partner link")
	if !ok {
		return
	}
	ctx := r.Context()
	err := h.inTx(ctx, eventID, partnerLinksLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		removed, err := q.DeleteEventPartnerLink(ctx, linkID, eventID)
		if err != nil {
			return err
		}
		if !removed {
			return pgx.ErrNoRows
		}
		if err := q.CompactEventPartnerLinks(ctx, eventID); err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.partner_link.delete", eventID, map[string]any{"link_id": linkID.String()})
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			partnerNotFound(w, r)
			return
		}
		h.failed(w, r, "delete partner link", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleReorderPartnerLinks serves PUT .../partner-links/order. link_ids must
// name every link of the event exactly once; the list is the new order.
func (h *ExtrasHandler) HandleReorderPartnerLinks(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	var req partnerOrderRequest
	if !readBody(w, r, &req) {
		return
	}
	ids, ok := parseOrderIDs(req.LinkIDs)
	if !ok {
		invalidOrder(w, r, "link_ids")
		return
	}
	ctx := r.Context()
	var rows []gen.EventPartnerLinkRow
	err := h.inTx(ctx, eventID, partnerLinksLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		cur, err := q.ListEventPartnerLinks(ctx, eventID)
		if err != nil {
			return err
		}
		have := make([]uuid.UUID, 0, len(cur))
		for _, c := range cur {
			have = append(have, c.ID)
		}
		if !idsMatch(have, ids) {
			return errOrderMismatch
		}
		if len(ids) > 0 {
			n, err := q.ReorderEventPartnerLinks(ctx, eventID, ids)
			if err != nil {
				return err
			}
			if n != int64(len(ids)) {
				return errOrderMismatch
			}
		}
		if rows, err = q.ListEventPartnerLinks(ctx, eventID); err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.partner_link.reorder", eventID, map[string]any{"count": len(ids)})
	})
	if errors.Is(err, errOrderMismatch) {
		orderMismatch(w, r, "event.partner_links_order_mismatch", "link_ids", "link")
		return
	}
	if err != nil {
		h.failed(w, r, "reorder partner links", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": partnerLinksJSON(rows), "max_items": eventmaterials.MaxPartnerLinks})
}

var errOrderMismatch = errors.New("hmaterials: the ids are not exactly the stored items of the event")
