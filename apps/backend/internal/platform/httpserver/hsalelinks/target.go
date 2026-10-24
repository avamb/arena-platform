package hsalelinks

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

// maxURLLength is the longest address a url link may carry (the column rule).
const maxURLLength = 2048

// apiError is a refusal a handler answers as it is.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) write(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, e.status, e.code, e.msg)
}

func invalidTarget(msg string) *apiError {
	return &apiError{http.StatusUnprocessableEntity, "sales_link.invalid_target", msg}
}

// resolvedTarget is a target that passed every check: ids are filled the way
// the table stores them (a session link also carries its event).
type resolvedTarget struct {
	kind       string
	eventID    *uuid.UUID
	sessionID  *uuid.UUID
	promoterID *uuid.UUID
	url        *string
	// campaign is the slug a new link's default utm_campaign is made from:
	// the event, else the promoter, else the organization.
	campaign string
}

func parseID(raw *string) (*uuid.UUID, bool) {
	if raw == nil {
		return nil, true
	}
	id, err := uuid.Parse(strings.TrimSpace(*raw))
	if err != nil {
		return nil, false
	}
	return &id, true
}

// validateURL accepts an absolute http or https address without credentials.
func validateURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxURLLength {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.User != nil {
		return "", false
	}
	return s, true
}

// resolveTarget checks a target body against the path organization. The shape
// is checked first (which ids a kind takes), then one query proves every named
// row belongs to the organization. A foreign or unknown target is a 422, never
// a 404, and a url that is not an http(s) address without credentials is a 400.
func (h *Handler) resolveTarget(ctx context.Context, orgID uuid.UUID, in targetInput) (resolvedTarget, *apiError, error) {
	kind := strings.TrimSpace(in.Kind)
	if !oneOf(kind, targetKinds) {
		return resolvedTarget{}, invalidTarget("target.kind must be one of org_page, promoter_page, event, session or url"), nil
	}
	eventID, ok1 := parseID(in.EventID)
	sessionID, ok2 := parseID(in.SessionID)
	promoterID, ok3 := parseID(in.PromoterID)
	if !ok1 || !ok2 || !ok3 {
		return resolvedTarget{}, invalidTarget("an id in the target is not a valid UUID"), nil
	}

	// Which ids a kind takes. Extra ids are refused rather than ignored: a link
	// must mean exactly what its caller wrote.
	var want struct{ event, session, promoter, url bool }
	switch kind {
	case salelinks.KindEvent:
		want.event = true
	case salelinks.KindSession:
		want.session = true
	case salelinks.KindPromoterPage:
		want.promoter = true
	case salelinks.KindURL:
		want.url = true
	}
	urlGiven := in.URL != nil && strings.TrimSpace(*in.URL) != ""
	// A session link may also repeat its event: it must then be the session's own.
	if (eventID != nil && !want.event && !want.session) ||
		(sessionID != nil) != want.session ||
		(promoterID != nil) != want.promoter ||
		(urlGiven && !want.url) ||
		(want.event && eventID == nil) {
		return resolvedTarget{}, invalidTarget("the target fields do not match target.kind: event needs event_id, session needs session_id, promoter_page needs promoter_id, url needs url, org_page takes none"), nil
	}

	rt := resolvedTarget{kind: kind, eventID: eventID, sessionID: sessionID, promoterID: promoterID}
	if want.url {
		clean, ok := "", false
		if urlGiven {
			clean, ok = validateURL(*in.URL)
		}
		if !ok {
			return resolvedTarget{}, &apiError{http.StatusBadRequest, "sales_link.invalid_url",
				"target.url must be an absolute http or https address without a user name or password, at most 2048 characters"}, nil
		}
		rt.url = &clean
	}

	facts, err := h.queries.CheckSalesLinkTarget(ctx, orgID, eventID, sessionID, promoterID)
	if err != nil {
		return resolvedTarget{}, nil, err
	}
	switch kind {
	case salelinks.KindEvent:
		if facts.EventID == nil {
			return resolvedTarget{}, invalidTarget("the event was not found in this organization"), nil
		}
	case salelinks.KindSession:
		if facts.EventID == nil || !facts.SessionOK {
			return resolvedTarget{}, invalidTarget("the session was not found in this organization"), nil
		}
		rt.eventID = facts.EventID
	case salelinks.KindPromoterPage:
		if !facts.PromoterOK {
			return resolvedTarget{}, invalidTarget("the promoter was not found in this organization"), nil
		}
	}

	switch {
	case (kind == salelinks.KindEvent || kind == salelinks.KindSession) && facts.EventSlug != "":
		rt.campaign = facts.EventSlug
	case kind == salelinks.KindPromoterPage && facts.PromoterSlug != "":
		rt.campaign = facts.PromoterSlug
	default:
		rt.campaign = facts.OrgSlug
	}
	return rt, nil, nil
}
