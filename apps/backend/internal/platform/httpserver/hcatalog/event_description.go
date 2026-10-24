// event_description.go - the description of an event on its single write path
// (spec 43, RT-02). REST create and patch take description (and the per-locale
// translations) plus the optional description_format ("text" by default,
// "art"), pass every text through richtext.Normalize with the description
// profile and store ART with the format flag "art". A refused text is
// 422 event.invalid_description with the richtext reason.
package hcatalog

import (
	"net/http"
	"strings"

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
