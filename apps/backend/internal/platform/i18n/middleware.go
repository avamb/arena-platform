// middleware.go provides the LocaleMiddleware net/http middleware that
// negotiates the request locale and stores a go-i18n Localizer in the
// request context.
//
// Locale negotiation follows the priority chain defined in app_spec.txt
// and implemented by NegotiateLocale in locale.go:
//
//  1. ?lang= query parameter (highest priority, explicit client override)
//  2. Accept-Language header  (RFC 7231 §5.3.5 with q-value parsing)
//  3. preferred               (caller-supplied user preference — stub for
//     this milestone; the real identity module populates it via AuthContext)
//  4. defaultLocale           (configured DEFAULT_LOCALE env variable, "en")
//
// The resulting Localizer is stored in the request context via
// WithLocalizer so handlers can call Localize(r.Context(), ...) without
// carrying the Bundle as a dependency.
package i18n

import "net/http"

// LocaleMiddleware returns a net/http middleware that:
//
//  1. Resolves the best-match locale from the request headers/params using
//     NegotiateLocale.
//  2. Creates a go-i18n Localizer bound to that locale.
//  3. Stores the Localizer in the request context via WithLocalizer.
//
// Parameters:
//   - b:             the Bundle to derive per-request Localizers from.
//     Must be non-nil; calling LocaleMiddleware with nil panics at
//     middleware construction time (not per-request) to fail fast.
//   - defaultLocale: the fallback locale when no supported locale can be
//     matched. Passed directly to NegotiateLocale (defaults to "en").
//   - supported:     the set of locale tags the server supports.  If empty,
//     NegotiateLocale falls back to SupportedLocales = ["en","ru"].
func LocaleMiddleware(b *Bundle, defaultLocale string, supported []string) func(http.Handler) http.Handler {
	if b == nil {
		// allow:panic: middleware-construction-time nil-dependency guard.
		// LocaleMiddleware is invoked once during router assembly at boot;
		// per-request handlers never reach this branch.
		panic("i18n.LocaleMiddleware: Bundle must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			acceptLang := r.Header.Get("Accept-Language")
			langParam := r.URL.Query().Get("lang")

			// Step 3 (preferred locale from AuthContext) is a stub for this
			// milestone — the real identity module will populate it in a later
			// milestone by reading auth.AuthContextFromContext(ctx).PreferredLocale.
			preferred := ""

			locale := NegotiateLocale(acceptLang, langParam, preferred, defaultLocale, supported)
			loc := b.LocalizerFor(locale)
			ctx := WithLocalizer(r.Context(), loc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
