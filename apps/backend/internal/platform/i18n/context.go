// context.go owns the per-request context plumbing and the Localize
// helper used by HTTP handlers to produce locale-aware messages.
//
// Usage pattern:
//
//	// In a handler:
//	msg := i18n.Localize(r.Context(), "error.not_found",
//	    "The requested resource does not exist.", nil)
//	writeJSON(w, 404, map[string]any{"error": msg})
//
// When LocaleMiddleware is wired, Localize returns the translated string
// for the negotiated locale. When the middleware is absent (e.g. in unit
// tests that only test non-i18n behaviour) it returns the fallback string
// so handlers produce correct output in both configurations.
package i18n

import (
	"context"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
)

type localizerKey struct{}

// WithLocalizer returns a context that carries the supplied Localizer.
// LocaleMiddleware calls this on every request so that downstream
// handlers can retrieve the localizer via LocalizerFrom or Localize
// without importing the middleware package.
func WithLocalizer(ctx context.Context, loc *goi18n.Localizer) context.Context {
	return context.WithValue(ctx, localizerKey{}, loc)
}

// LocalizerFrom extracts the Localizer stored by WithLocalizer.
// Returns (nil, false) when no localizer has been installed — for example
// in unit tests that do not wire LocaleMiddleware.
func LocalizerFrom(ctx context.Context) (*goi18n.Localizer, bool) {
	loc, ok := ctx.Value(localizerKey{}).(*goi18n.Localizer)
	return loc, ok && loc != nil
}

// Localize returns the localized string for messageID using the Localizer
// stored in ctx.  If no localizer is in ctx, or the message lookup fails,
// fallback is returned unchanged.
//
// Parameters:
//   - ctx:          the request context (populated by LocaleMiddleware).
//   - messageID:    the dotted message key (e.g. "error.not_found").
//   - fallback:     the default English string returned when no localizer
//     is wired or the key is absent from the catalog.
//   - templateData: optional template variables for messages containing
//     {{.Variable}} placeholders; pass nil for static messages.
//
// The fallback parameter ensures handlers remain correct in test
// environments and in deployments where the locale middleware is not
// wired — there is no silent empty-string failure mode.
func Localize(ctx context.Context, messageID string, fallback string, templateData map[string]any) string {
	loc, ok := LocalizerFrom(ctx)
	if !ok {
		return fallback
	}

	var td interface{}
	if len(templateData) > 0 {
		td = templateData
	}

	msg, err := loc.Localize(&goi18n.LocalizeConfig{
		MessageID:    messageID,
		TemplateData: td,
	})
	if err != nil || msg == "" {
		return fallback
	}
	return msg
}
