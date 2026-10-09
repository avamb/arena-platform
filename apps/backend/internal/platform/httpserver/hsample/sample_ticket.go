// Package hsample renders the sample e-ticket of a session (migration 0119):
// GET /v1/organizations/{org_id}/sessions/{session_id}/sample-ticket.
//
// An organizer who just published an event through the Telegram event
// center gets, right in the chat, the PDF a buyer will receive — the real
// layout, the real fonts, the organization's logo and the event's poster,
// the first category's price — stamped SAMPLE across the details. The
// barcode is a REAL platform EAN-13 minted through the same cross-authority
// uniqueness guard as a sold ticket's (internal/platform/barcodes/mint), so
// it scans at the gate; it lives in the 'sample' barcode authority, which
// SCAN_TICKET recognises and answers with "sample ticket" instead of
// admitting anyone. One code per session, reused on every request.
package hsample

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/mint"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// SampleAuthorityType is the barcode_authorities.type of sample codes.
const SampleAuthorityType = "sample"

// mediaFetchTimeout bounds each artwork fetch; the PDF ships without the
// picture rather than waiting on a slow store.
const mediaFetchTimeout = 5 * time.Second

// Handler renders sample tickets.
type Handler struct {
	queries *gen.Queries
	// media resolves the organization's logo and the event's poster; nil
	// renders the ticket without artwork.
	media  delivery.MediaResolver
	logger *slog.Logger
}

// New builds a Handler. media may be nil.
func New(queries *gen.Queries, media delivery.MediaResolver, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{queries: queries, media: media, logger: logger}
}

// words is what the sample prints where a real ticket prints the buyer's
// data: the stamp, the ticket number and the holder line.
type words struct {
	Stamp  string
	Holder string
}

var wordsByLocale = map[string]words{
	"en": {Stamp: "SAMPLE", Holder: "Sample: buyer's name"},
	"ru": {Stamp: "ОБРАЗЕЦ", Holder: "Образец: имя покупателя"},
	"cs": {Stamp: "VZOR", Holder: "Vzor: jméno kupujícího"},
	"es": {Stamp: "MUESTRA", Holder: "Muestra: nombre del comprador"},
}

// resolveLocale picks the PDF's language: the caller's `locale` query
// parameter (the organizer's bot language), else the organization's default,
// else English. Only a language the renderer prints labels for counts.
func resolveLocale(query, orgDefault string) string {
	for _, raw := range []string{query, orgDefault} {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if i := strings.IndexAny(tag, "-_"); i > 0 {
			tag = tag[:i]
		}
		if _, ok := wordsByLocale[tag]; ok {
			return tag
		}
	}
	return "en"
}

// HandleSessionSampleTicket answers the session's sample PDF. Membership in
// the organization is enforced by the caller (the server's shim); here the
// session is looked up scoped to the org so a foreign session is a plain 404.
func (h *Handler) HandleSessionSampleTicket(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "sample tickets are not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	ctx := r.Context()

	pres, err := h.queries.GetSampleTicketPresentation(ctx, sessionID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound,
			httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return
	}
	fail := func(what string, err error) {
		h.logger.Error("hsample: sample ticket failed",
			slog.String("part", what), slog.String("session_id", sessionID.String()), slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("sample_ticket.internal", "failed to render the sample ticket", r))
	}
	if err != nil {
		fail("presentation", err)
		return
	}

	code, err := h.ensureSampleCode(ctx, sessionID)
	if err != nil {
		fail("barcode", err)
		return
	}

	locale := resolveLocale(r.URL.Query().Get("locale"), pres.OrgLocale)
	t := h.buildTicket(ctx, pres, code, locale)
	out, err := pdf.Render(ctx, t)
	if err != nil {
		fail("render", err)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="sample-ticket.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: gosec's taint analysis flags this only because the
	// locale entered through r.URL.Query(). It is matched against a fixed
	// four-entry table before use and never reaches the body; every byte
	// written here is a PDF the renderer built from stored rows.
	_, _ = w.Write(out)
}

// ensureSampleCode returns the session's sample EAN-13, minting it on the
// first request. Two concurrent first requests both mint; the loser's
// link insert is a no-op and it answers with the winner's code (the spare
// barcode row stays reserved, which is harmless).
func (h *Handler) ensureSampleCode(ctx context.Context, sessionID uuid.UUID) (string, error) {
	if row, err := h.queries.GetSessionSampleBarcode(ctx, sessionID); err == nil {
		return row.EAN13, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("read sample barcode: %w", err)
	}
	authority, err := h.queries.GetBarcodeAuthorityByType(ctx, SampleAuthorityType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("the 'sample' barcode authority is missing: migration 0119 not applied")
		}
		return "", fmt.Errorf("read sample authority: %w", err)
	}
	code, err := mint.EAN13(ctx, h.queries, authority.ID, nil, nil)
	if err != nil {
		return "", err
	}
	barcode, err := h.queries.GetBarcodeByExternalRefAny(ctx, code)
	if err != nil {
		return "", fmt.Errorf("read minted sample barcode %s: %w", code, err)
	}
	won, err := h.queries.InsertSessionSampleBarcode(ctx, sessionID, barcode.ID, code)
	if err != nil {
		return "", fmt.Errorf("link sample barcode: %w", err)
	}
	if won {
		return code, nil
	}
	row, err := h.queries.GetSessionSampleBarcode(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("re-read sample barcode after a lost race: %w", err)
	}
	return row.EAN13, nil
}

// buildTicket maps the presentation onto the renderer's Ticket exactly as
// the delivery worker does for a sold ticket (delivery.renderTicketPDF),
// with the buyer's data replaced by the sample words.
func (h *Handler) buildTicket(ctx context.Context, pres gen.SampleTicketPresentationRow, code, locale string) pdf.Ticket {
	wd := wordsByLocale[locale]
	t := pdf.Ticket{
		// The id only keys in-document image resources; the session id is
		// as good as any and never printed.
		TicketID:               pres.SessionID.String(),
		TicketNumber:           wd.Stamp,
		Watermark:              wd.Stamp,
		Locale:                 locale,
		EventName:              pres.EventName,
		SessionStart:           pres.StartAt,
		DoorsOpenAt:            pres.DoorsOpenAt,
		SessionTZ:              deref(pres.VenueTimezone),
		VenueName:              deref(pres.VenueName),
		VenueAddress:           deref(pres.VenueAddress),
		VenueCity:              deref(pres.VenueCity),
		HolderName:             wd.Holder,
		EAN13:                  code,
		OrgName:                pres.OrgName,
		OrganizerName:          deref(pres.PromoterName),
		OrgWebsiteURL:          deref(pres.WebsiteURL),
		LegalName:              deref(pres.LegalName),
		LegalAddressLine1:      deref(pres.LegalAddressLine1),
		LegalAddressLine2:      deref(pres.LegalAddressLine2),
		LegalAddressPostalCode: deref(pres.LegalAddressPostalCode),
		LegalAddressCity:       deref(pres.LegalAddressCity),
		LegalAddressCountry:    deref(pres.LegalAddressCountry),
		ContactEmail:           deref(pres.ContactEmail),
	}
	if tier, ok := h.firstCategory(ctx, pres.SessionID); ok {
		t.TierName = tier.Name
		price := tier.PriceAmount
		t.PriceMinor = &price
		t.Currency = tier.Currency
	}
	if pres.LogoMediaID != nil {
		t.OrgLogo = h.artwork(ctx, *pres.LogoMediaID, "logo")
	}
	if pres.PosterMediaID != nil {
		t.PosterImage = h.artwork(ctx, *pres.PosterMediaID, "poster")
	}
	return t
}

// firstCategory is the category a buyer sees first: the open one with the
// lowest sort order, or the first one at all when none is open.
func (h *Handler) firstCategory(ctx context.Context, sessionID uuid.UUID) (gen.TicketTierRow, bool) {
	tiers, err := h.queries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil || len(tiers) == 0 {
		if err != nil {
			h.logger.Warn("hsample: list categories failed", slog.String("session_id", sessionID.String()), slog.Any("error", err))
		}
		return gen.TicketTierRow{}, false
	}
	for _, tt := range tiers {
		if tt.IsOpen {
			return tt, true
		}
	}
	return tiers[0], true
}

// artwork fetches one image through the media resolver, bounded in time,
// and drops it silently when it is missing or not a format the renderer
// embeds — the sample must never fail over a picture.
func (h *Handler) artwork(ctx context.Context, mediaID uuid.UUID, what string) []byte {
	if h.media == nil {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, mediaFetchTimeout)
	defer cancel()
	raw, _, err := h.media.ResolveLogo(fetchCtx, mediaID.String())
	if err != nil {
		if !errors.Is(err, delivery.ErrLogoNotFound) {
			h.logger.Warn("hsample: "+what+" fetch failed", slog.String("media_id", mediaID.String()), slog.Any("error", err))
		}
		return nil
	}
	if !pdf.SupportsImage(raw) {
		return nil
	}
	return raw
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
