package hscannerapp

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/qr"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// maxBodyBytes is the largest JSON body of the scanner routes.
const maxBodyBytes = 64 * 1024

// qrSize is the side of the QR picture of a pairing link, in pixels.
const qrSize = 320

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, message, r))
}

func writeNoDB(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "database is unavailable")
}

// readJSON decodes one JSON object of at most maxBodyBytes into dst. Unknown
// members are ignored, so a newer app can send more than this server reads.
func readJSON(r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes || strings.TrimSpace(string(raw)) == "" {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(dst); err != nil || dec.More() {
		return false
	}
	return true
}

// writeAudit records one audit event; a failure is logged, never answered.
func (h *Handler) writeAudit(r *http.Request, actorType, actorID, action, resourceType, resourceID string, metadata map[string]any) {
	if h.Audit == nil {
		return
	}
	ctx := r.Context()
	ev := audit.Event{
		OccurredAt:   h.now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.TrustedClientIP(r, h.TrustedProxies),
		Metadata:     metadata,
	}
	if err := h.Audit.Write(ctx, ev); err != nil {
		h.Logger.Warn("hscannerapp: audit write failed", "action", action, "error", err.Error())
	}
}

// pathOrg reads {org_id}; the router's membership gate already parsed it.
func pathOrg(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "org_id")))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"org.invalid_id", "org_id is not a UUID", r, map[string]any{"field": "org_id"}))
		return uuid.Nil, false
	}
	return id, true
}

// callerUser is the signed-in person of the request. Any other caller (an
// API key cannot reach these routes: their permissions are never on a key)
// is 403 scanner.user_only.
func callerUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if ok && actor.Type == auth.ActorTypeUser {
		if id, err := uuid.Parse(actor.ID); err == nil {
			return id, true
		}
	}
	writeErr(w, r, http.StatusForbidden, "scanner.user_only", "only a signed-in person can use this route")
	return uuid.Nil, false
}

// canManage reports whether the caller holds scanner_pass.manage in the path
// organization (the router put the organization scope in the context).
func (h *Handler) canManage(r *http.Request) bool {
	return h.Perms != nil && h.Perms.Check(r.Context(), "scanner_pass.manage", "scanner_devices") == nil
}

// PairingLink is a pairing code as the organizer gets it.
type PairingLink struct {
	Code string `json:"code"`
	Link string `json:"link"`
	// ExpiresAt is set for an organizer code only.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// QRPNG is the base64 PNG of the link; null when it could not be drawn.
	QRPNG *string `json:"qr_png"`
}

func (h *Handler) pairingLink(code string) PairingLink {
	link := scannerapp.PairLink(h.APIBaseURL, code)
	out := PairingLink{Code: code, Link: link}
	if png, err := qr.PNG(link, qrSize, 4); err == nil {
		s := base64.StdEncoding.EncodeToString(png)
		out.QRPNG = &s
	} else {
		h.Logger.Warn("hscannerapp: render QR failed", "error", err.Error())
	}
	return out
}
