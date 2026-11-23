// public_translations.go — spec 08_architecture/63 §16.3: the public feed
// serves an event's organizer translation when ?locale=xx names a language
// that has one. One batched read of the originals and one of the translations
// (contenttranslate.ListForOwners) serve a whole list, no read per event.
package hfeed

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/contenttranslate"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// defaultContentLocale is what an event without a readable content_locale is
// reported as (the column default of migration 0220).
const defaultContentLocale = "en"

// eventOriginal is what the feed needs of an event beyond its row: the
// language of the original text and the photo-report text.
type eventOriginal struct {
	ContentLocale  string
	ReportTextHTML *string
	// ReportOpen is true once the event has ended; the report text follows the
	// rule of the photo report and stays hidden before that.
	ReportOpen bool
}

// requestedLocale reads ?locale=. An empty, unknown or unsupported value gives
// "" (the original is served, never a 400).
func requestedLocale(r *http.Request) string {
	raw := strings.TrimSpace(r.URL.Query().Get("locale"))
	if raw == "" {
		return ""
	}
	loc, ok := contenttranslate.NormalizeLocale(raw)
	if !ok {
		return ""
	}
	return loc
}

func nonBlank(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	return p
}

// applyTranslation fills the three translation fields of resp and, when a
// translation of the wanted locale exists, replaces name, short_description,
// description_html and description_text with it. resp must already carry the
// ORIGINAL texts (description_html included): their hash is compared with the
// translation's source_hash. It returns the photo-report text to serve
// (translated or original; nil while the report is not open or empty).
func applyTranslation(resp *publicFeedEventResponse, orig eventOriginal, trs []contenttranslate.Translation, locale string) *string {
	origLocale := orig.ContentLocale
	if origLocale == "" {
		origLocale = defaultContentLocale
	}
	resp.ContentLocale = origLocale
	resp.TranslationStale = false
	avail := []string{}
	for _, t := range trs {
		if t.Locale != origLocale && !slices.Contains(avail, t.Locale) {
			avail = append(avail, t.Locale)
		}
	}
	slices.Sort(avail)
	resp.AvailableLocales = append([]string{origLocale}, avail...)

	report := orig.ReportTextHTML
	if locale != "" && locale != origLocale {
		for _, t := range trs {
			if t.Locale != locale {
				continue
			}
			name := resp.Name
			cur := contenttranslate.SourceHash(contenttranslate.Fields{
				Name:             &name,
				ShortDescription: resp.ShortDescription,
				DescriptionHTML:  nonBlank(resp.DescriptionHTML),
				ReportTextHTML:   orig.ReportTextHTML,
			})
			resp.TranslationStale = contenttranslate.Stale(t, cur)
			resp.ContentLocale = locale
			if t.Fields.Name != nil && strings.TrimSpace(*t.Fields.Name) != "" {
				resp.Name = *t.Fields.Name
			}
			if t.Fields.ShortDescription != nil {
				resp.ShortDescription = t.Fields.ShortDescription
			}
			if t.Fields.DescriptionHTML != nil {
				v := eventtext.ViewDescription(*t.Fields.DescriptionHTML, richtext.FormatART)
				resp.DescriptionHTML, resp.DescriptionText = &v.HTML, &v.Text
			}
			if t.Fields.ReportTextHTML != nil {
				v := eventtext.ViewDescription(*t.Fields.ReportTextHTML, richtext.FormatART)
				report = &v.HTML
			}
			break
		}
	}
	if !orig.ReportOpen {
		return nil
	}
	return nonBlank(report)
}

// hydrateFeedTranslations sets content_locale, available_locales and
// translation_stale on every response and serves the translation of locale
// where there is one. It returns the report text to serve, keyed by event id.
// Non-fatal: a failed read leaves the original texts with content_locale "en".
func (h *Handler) hydrateFeedTranslations(ctx context.Context, responses []publicFeedEventResponse, locale string) map[uuid.UUID]*string {
	reports := map[uuid.UUID]*string{}
	if h.publicFeedQueries == nil || len(responses) == 0 {
		return reports
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, resp := range responses {
		if id, err := uuid.Parse(resp.ID); err == nil {
			ids = append(ids, id)
		}
	}
	db := h.publicFeedQueries.DB()
	originals := map[uuid.UUID]eventOriginal{}
	rows, err := db.Query(ctx, `SELECT id, content_locale, report_text_html,
       (last_session_at IS NOT NULL AND last_session_at <= $2)
  FROM events WHERE id = ANY($1)`, ids, time.Now().UTC())
	if err != nil {
		h.logger.Warn("public_feed: content locale read failed", slog.String("error", err.Error()))
	} else {
		for rows.Next() {
			var id uuid.UUID
			var o eventOriginal
			if err := rows.Scan(&id, &o.ContentLocale, &o.ReportTextHTML, &o.ReportOpen); err != nil {
				h.logger.Warn("public_feed: content locale scan failed", slog.String("error", err.Error()))
				break
			}
			originals[id] = o
		}
		rows.Close()
	}
	trs, err := contenttranslate.ListForOwners(ctx, db, contenttranslate.OwnerEvent, ids)
	if err != nil {
		h.logger.Warn("public_feed: translations read failed", slog.String("error", err.Error()))
		trs = nil
		locale = ""
	}
	for i := range responses {
		id, err := uuid.Parse(responses[i].ID)
		if err != nil {
			continue
		}
		reports[id] = applyTranslation(&responses[i], originals[id], trs[id], locale)
	}
	return reports
}
