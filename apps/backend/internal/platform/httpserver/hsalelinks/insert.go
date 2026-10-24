package hsalelinks

// insert.go — task QL-13 (spec 43 section 6.5): the printable PDF insert of
// a sales link, the `format=pdf` branch of GET .../sales-links/{id}/qr. The
// page is rendered by salelinks/pdfinsert from the link's short address and
// the names GetSalesLinkInsertFacts reads: the event (or the date of a
// session, or the promoter) the code sells, the organization and its logo.
// The one localized line comes from the shared i18n bundle in the language
// the request negotiates (?locale=, Accept-Language, the organization's
// default); without a bundle it is English.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/pdfinsert"
)

// LogoResolver is the slice of delivery.MediaResolver the insert needs: the
// bytes of a media object by id. delivery/mediaresolver implements it.
type LogoResolver interface {
	ResolveLogo(ctx context.Context, mediaID string) (bytes []byte, signedURL string, err error)
}

// logoFetchTimeout bounds the logo read: a slow media store must not hold the
// insert, the page prints the organization's name instead.
const logoFetchTimeout = 5 * time.Second

// WithInsert wires the dependencies of the PDF insert. Both may be nil: the
// line is then English and the page carries no logo.
func (h *Handler) WithInsert(bundle *i18n.Bundle, logos LogoResolver) *Handler {
	h.bundle = bundle
	h.logos = logos
	return h
}

// insertLocale negotiates the language of the insert: ?locale= (or ?lang=),
// then Accept-Language, then the organization's default locale.
func insertLocale(r *http.Request, orgDefault string) string {
	lang := r.URL.Query().Get("locale")
	if lang == "" {
		lang = r.URL.Query().Get("lang")
	}
	return i18n.NegotiateLocale(r.Header.Get("Accept-Language"), lang, orgDefault, pdfinsert.DefaultLocale, pdfinsert.SupportedLocales)
}

// insertBody renders the PDF insert of row on paper.
func (h *Handler) insertBody(ctx context.Context, r *http.Request, row gen.SalesLinkRow, paper pdfinsert.Paper) ([]byte, error) {
	facts, err := h.queries.GetSalesLinkInsertFacts(ctx, row.OrgID, row.EventID, row.SessionID, row.PromoterID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// pgx.ErrNoRows: the organization is gone; the insert still prints the
	// address, with no names.

	title, subtitle := "", ""
	switch row.TargetKind {
	case salelinks.KindSession:
		title = facts.EventName
		if facts.SessionStartAt != nil {
			subtitle = pdfinsert.SessionLine(*facts.SessionStartAt, facts.VenueTimezone)
		}
	case salelinks.KindEvent:
		title = facts.EventName
	case salelinks.KindPromoterPage:
		title = facts.PromoterName
	}
	orgName := strings.TrimSpace(facts.OrgName)
	if strings.TrimSpace(title) == "" {
		title = orgName
	}
	logo := h.fetchLogo(ctx, facts.OrgLogoMediaID)
	top := orgName
	if len(logo) == 0 && strings.EqualFold(strings.TrimSpace(title), orgName) {
		// The organization's own page: its name is the title already.
		top = ""
	}
	locale := insertLocale(r, facts.OrgDefaultLocale)
	content := salelinks.ShortURL(h.shortBase, row.Code)
	return pdfinsert.Render(pdfinsert.Insert{
		Content:  content,
		Title:    title,
		Subtitle: subtitle,
		Tagline:  pdfinsert.Tagline(h.bundle, locale),
		OrgName:  top,
		Logo:     logo,
		Locale:   locale,
		Paper:    paper,
	})
}

// fetchLogo reads the organization logo; every failure is logged and gives
// no logo, never an error, because the page prints without it.
func (h *Handler) fetchLogo(ctx context.Context, mediaID *uuid.UUID) []byte {
	if h.logos == nil || mediaID == nil {
		return nil
	}
	id := mediaID.String()
	lctx, cancel := context.WithTimeout(ctx, logoFetchTimeout)
	defer cancel()
	b, _, err := h.logos.ResolveLogo(lctx, id)
	if err != nil {
		h.logger.Warn("sales links: insert logo not used", slog.String("media_id", id), slog.String("error", err.Error()))
		return nil
	}
	return b
}
