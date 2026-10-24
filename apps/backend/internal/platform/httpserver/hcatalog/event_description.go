// event_description.go - the description of an event on its single write path
// (spec 43, RT-02). REST create and patch take description (and the per-locale
// translations) plus the optional description_format ("text" by default,
// "art"), pass every text through richtext.Normalize with the description
// profile and store ART with the format flag "art". A refused text is
// 422 event.invalid_description with the richtext reason.
package hcatalog

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// eventTranslation is one locale of the translations map of create and patch.
type eventTranslation struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// parseTextFormat reads a format field. A value that is neither "", "text" nor
// "art" is answered 400 with code and false is returned.
func parseTextFormat(w http.ResponseWriter, r *http.Request, raw, field, code string) (string, bool) {
	format, ok := eventtext.ParseFormat(strings.TrimSpace(raw))
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			code, field+" must be one of: text, art", r, map[string]any{"field": field},
		))
		return "", false
	}
	return format, true
}

func writeInvalidDescription(w http.ResponseWriter, r *http.Request, field, locale string, err error) {
	details := map[string]any{"field": field, "reason": eventtext.Reason(err)}
	if locale != "" {
		details["locale"] = locale
	}
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_description", "the event description is not allowed: "+err.Error(), r, details,
	))
}

// normalizeTranslationDescriptions turns the translated descriptions of a
// request into ART. Blank ones are left out (as before). A refused text has
// been answered (422) and ok is false.
func normalizeTranslationDescriptions(w http.ResponseWriter, r *http.Request, translations map[string]eventTranslation, format string) (out map[string]string, ok bool) {
	for locale, trans := range translations {
		locale = strings.TrimSpace(locale)
		desc := strings.TrimSpace(trans.Description)
		if locale == "" || desc == "" {
			continue
		}
		clean, err := eventtext.NormalizeDescription(desc, format)
		if err != nil {
			writeInvalidDescription(w, r, "translations."+locale+".description", locale, err)
			return nil, false
		}
		if clean == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(translations))
		}
		out[locale] = clean
	}
	return out, true
}

func localesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for l := range m {
		out = append(out, l)
	}
	return out
}

// hydrateDescriptionFormats fills DescriptionFormat from one batched query.
// Like the other hydrations it is presentational: a failure logs and leaves
// the field empty, which a reader must treat as unknown.
//
// The format is read by a second statement, not by the query that read the
// description (the format is not in the shared event SELECTs, and this batch
// costs one round trip for the whole list). A PATCH or a backfill that
// converts this event between the two statements can give ONE response the
// old text with the new format, or the reverse. For display that is harmless:
// the views are rebuilt by the sanitiser from whatever it gets, so nothing
// unsafe can come out of it, the worst case is tags shown as words (or marks
// lost) in that one response, and the next request is right. Nothing is
// written back from these views. A reader that WRITES back what it read (the
// event-bundle export) reads both in one RepeatableRead snapshot instead.
func (h *Handler) hydrateDescriptionFormats(ctx context.Context, responses []eventResponse) {
	if h.eventQueries == nil || len(responses) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, r := range responses {
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	formats, err := h.eventQueries.ListEventDescriptionFormats(ctx, ids)
	if err != nil {
		h.logger.Warn("event: description format hydration failed", slog.String("error", err.Error()))
		return
	}
	for i := range responses {
		if id, err := uuid.Parse(responses[i].ID); err == nil {
			responses[i].DescriptionFormat = formats[id]
		}
		responses[i].fillDescriptionViews()
	}
}

// fillDescriptionViews sets DescriptionHTML/Text/Legacy from Description and
// DescriptionFormat. The description is already resolved for the request's
// locale (COALESCE of the translation, English and the base), and the event's
// flag covers every translation, so one format reads them all. With no
// description, or a format that was not hydrated, the views stay nil: the
// server never guesses a format.
func (r *eventResponse) fillDescriptionViews() {
	r.DescriptionHTML, r.DescriptionText, r.DescriptionLegacy = nil, nil, nil
	if r.Description == nil || r.DescriptionFormat == "" {
		return
	}
	v := eventtext.ViewDescription(*r.Description, r.DescriptionFormat)
	r.DescriptionHTML, r.DescriptionText, r.DescriptionLegacy = &v.HTML, &v.Text, &v.Legacy
}
