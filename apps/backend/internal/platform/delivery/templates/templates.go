// Package templates renders localized email bodies and subjects for the
// ticket-deliver worker (feature #289, T-2).
//
// Templates live next to this file as <name>.<locale>.tmpl, where:
//
//   - name   ∈ {"ticket", "invitation"}  (the email kind)
//   - locale ∈ {"en", "de", "es", "he"}  (the AllPay markets baseline)
//
// Each .tmpl file defines three blocks via Go's html/template "define" syntax:
//
//	{{define "subject"}} ... {{end}}    plain-text subject line
//	{{define "html"}}    ... {{end}}    HTML body
//	{{define "text"}}    ... {{end}}    plain-text fallback body
//
// Locale fallback: an unknown locale falls back to "en". Adding a new
// locale (for example "fr") is a two-file change: drop ticket.fr.tmpl and
// invitation.fr.tmpl into this directory; they are picked up automatically
// at process start because templates are embedded via embed.FS.
//
// The renderer is intentionally a thin layer: it owns only the embedded
// templates and the locale-fallback rules. The worker handler keeps
// responsibility for resolving data, attaching the PDF, and calling the
// SMTP adapter.
package templates

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strings"
	texttemplate "text/template"
)

//go:embed *.tmpl
var templateFS embed.FS

// DefaultLocale is used when the requested locale is empty or not in
// SupportedLocales.
const DefaultLocale = "en"

// SupportedLocales lists the locales that this package ships templates for.
// Order is alphabetical; tests assert this list matches the embedded files.
var SupportedLocales = []string{"de", "en", "es", "he"}

// TemplateKindTicket is the standard paid/free-checkout ticket delivery email.
const TemplateKindTicket = "ticket"

// TemplateKindInvitation is the complimentary invitation email (feature #149).
const TemplateKindInvitation = "invitation"

// Data is the value passed to every template. Fields are intentionally
// strings (not domain structs) so the template author can ignore nil checks
// and {{with}} blocks handle the "empty means omit" presentation rule.
type Data struct {
	// TicketID is the printable ticket UUID.
	TicketID string
	// RecipientEmail is the address the email is being sent to.
	RecipientEmail string
	// HolderName is the printed ticket holder name; may be empty.
	HolderName string
	// EventName is the human-readable event name (always required).
	EventName string
	// SessionStart is a pre-formatted "YYYY-MM-DD HH:MM (zone)" string in
	// the venue's local timezone, or empty if unknown.
	SessionStart string
	// VenueName may be empty if the venue is not yet resolved.
	VenueName string
	// TierName may be empty for GA / untiered tickets.
	TierName string

	// SeatSector / SeatRow / SeatNumber are the denormalized seat
	// coordinates copied from tickets.seat_sector / seat_row / seat_number
	// (SEAT-C3, feature #311). All three are empty for general-admission
	// tickets, in which case the templates omit the Sector / Row / Seat
	// rows entirely via {{with}} guards. All three are populated together
	// for tickets issued from an assigned-seats reservation.
	SeatSector string
	SeatRow    string
	SeatNumber string

	// Branding is the organisation branding block applied to the header
	// (logo + display name + website) and footer (legal identification).
	// Required for EU "commercial communications" minimum identification.
	// Callers must populate this with resolved values; the worker handler
	// is responsible for the org-logo / platform-logo fallback.
	Branding Branding
}

// Branding carries the organisation branding fields rendered into the
// header and footer of every transactional email and PDF e-ticket
// (feature #290, T-3).
//
// All fields are pre-resolved strings: the templates and pdf renderers
// perform no media or organisation lookups. The worker handler resolves
// LogoURL via the media adapter (or substitutes the platform fallback
// when the organisation has no logo_media_id).
//
// Legal-identification fields back the EU minimum-identification rule
// for commercial communications and unsubscribe/contact footers.
type Branding struct {
	// OrgName is the public display name printed in the header next to
	// the logo. Empty falls back to PlatformOrgName.
	OrgName string
	// WebsiteURL is the organisation website rendered as a footer link.
	// Empty means "omit the link"; the {{with}} guards collapse the row.
	WebsiteURL string
	// LogoURL is a fully-qualified URL (signed media URL or platform
	// fallback) used as the <img src> in the email header. Empty means
	// "render the wordmark only" — the {{with}} guard suppresses the
	// <img>. When LogoURL is empty the worker handler MUST set this to
	// PlatformLogoURL so the header always carries a logo.
	LogoURL string
	// LogoAlt is the accessible alt text for LogoURL. Defaults to OrgName
	// (or PlatformOrgName) when empty.
	LogoAlt string

	// Footer / EU minimum-identification fields.

	// LegalName is the registered juridical name of the organisation.
	// Required for the footer's commercial-communications block; empty
	// falls back to PlatformLegalName so the footer still identifies a
	// real legal entity.
	LegalName              string
	LegalAddressLine1      string
	LegalAddressLine2      string
	LegalAddressPostalCode string
	LegalAddressCity       string
	// LegalAddressCountry is ISO-3166-1 alpha-2 (e.g. "DE", "IL").
	LegalAddressCountry string
	// ContactEmail is the public contact address printed in the footer.
	// Empty falls back to PlatformContactEmail.
	ContactEmail string
}

// Platform defaults used by the worker handler when the organisation
// itself has no branding fields (logo_media_id IS NULL, legal_name IS
// NULL, etc.). The templates themselves never substitute these
// values — the renderer prints exactly what it is given.
const (
	PlatformOrgName      = "Arena Platform"
	PlatformLogoURL      = "https://assets.arena.example.com/branding/platform-logo.png"
	PlatformLegalName    = "Arena Platform"
	PlatformContactEmail = "support@arena.example.com"
)

// Rendered is the output of a single Render call: the three pieces of the
// email body that the SMTP adapter needs.
type Rendered struct {
	Subject  string
	HTMLBody string
	TextBody string
}

// Renderer holds parsed templates keyed by "<kind>.<locale>". It is safe
// for concurrent use after construction.
//
// Each (kind, locale) pair gets its own *html.Template and *text.Template
// pair so that the "subject"/"html"/"text" blocks defined inside one file
// do not overwrite the same-named blocks in another file (Go templates
// merge define-blocks into a shared namespace inside a single root).
type Renderer struct {
	html map[string]*htmltemplate.Template
	text map[string]*texttemplate.Template
	// known is the set of "<kind>.<locale>" pairs that were actually
	// parsed. Used by ResolveLocale to fall back when a locale is missing.
	known map[string]struct{}
}

// New parses every embedded template. Returns an error only if a file is
// malformed — in normal operation, the templates have been validated at
// build time so this never fails in production.
//
// HTML templates are parsed with html/template (auto-escaping). The
// text-body and subject use text/template (no escaping — they are
// already plain text).
func New() (*Renderer, error) {
	entries, err := templateFS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("templates: read embed.FS: %w", err)
	}

	htmlByKey := make(map[string]*htmltemplate.Template)
	textByKey := make(map[string]*texttemplate.Template)
	known := make(map[string]struct{})

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmpl") {
			continue
		}
		body, err := templateFS.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("templates: read %s: %w", e.Name(), err)
		}
		key := strings.TrimSuffix(e.Name(), ".tmpl") // "ticket.en"

		// Fresh per-file template set so define-blocks do not collide
		// across files.
		htmlT := htmltemplate.New(key)
		if _, err := htmlT.Parse(string(body)); err != nil {
			return nil, fmt.Errorf("templates: parse html %s: %w", e.Name(), err)
		}
		textT := texttemplate.New(key)
		if _, err := textT.Parse(string(body)); err != nil {
			return nil, fmt.Errorf("templates: parse text %s: %w", e.Name(), err)
		}

		// Make sure every file declares all three blocks.
		for _, block := range []string{"subject", "html", "text"} {
			if htmlT.Lookup(block) == nil {
				return nil, fmt.Errorf(
					"templates: %s missing required block %q", e.Name(), block,
				)
			}
		}

		htmlByKey[key] = htmlT
		textByKey[key] = textT
		known[key] = struct{}{}
	}

	if len(known) == 0 {
		return nil, fmt.Errorf("templates: no .tmpl files found in embed.FS")
	}

	return &Renderer{html: htmlByKey, text: textByKey, known: known}, nil
}

// ResolveLocale returns locale when a template for it exists, or
// DefaultLocale otherwise. kind is required because, in theory, a new
// kind could be added without yet shipping every locale for it.
func (r *Renderer) ResolveLocale(kind, locale string) string {
	locale = normalize(locale)
	if locale != "" {
		if _, ok := r.known[kind+"."+locale]; ok {
			return locale
		}
	}
	return DefaultLocale
}

// Render renders the three template blocks for (kind, locale).
// Falls back to DefaultLocale when locale is missing. Returns an error
// only if kind is unknown or the templates fail to execute.
func (r *Renderer) Render(kind, locale string, data Data) (Rendered, error) {
	resolved := r.ResolveLocale(kind, locale)
	key := kind + "." + resolved

	if _, ok := r.known[key]; !ok {
		return Rendered{}, fmt.Errorf(
			"templates: unknown template kind %q (locale %q)", kind, locale,
		)
	}

	// Subject and text body come from the text engine (no HTML escaping).
	subject, err := r.execText(key, "subject", data)
	if err != nil {
		return Rendered{}, fmt.Errorf("templates: render subject: %w", err)
	}
	textBody, err := r.execText(key, "text", data)
	if err != nil {
		return Rendered{}, fmt.Errorf("templates: render text: %w", err)
	}

	// HTML body comes from the html engine (auto-escaped).
	htmlBody, err := r.execHTML(key, "html", data)
	if err != nil {
		return Rendered{}, fmt.Errorf("templates: render html: %w", err)
	}

	return Rendered{
		Subject:  strings.TrimSpace(subject),
		HTMLBody: htmlBody,
		TextBody: textBody,
	}, nil
}

func (r *Renderer) execText(key, block string, data Data) (string, error) {
	t, ok := r.text[key]
	if !ok {
		return "", fmt.Errorf("missing text template %q", key)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (r *Renderer) execHTML(key, block string, data Data) (string, error) {
	t, ok := r.html[key]
	if !ok {
		return "", fmt.Errorf("missing html template %q", key)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// KnownTemplates returns the sorted list of "<kind>.<locale>" pairs that
// were parsed at construction. Useful for diagnostics and tests.
func (r *Renderer) KnownTemplates() []string {
	out := make([]string, 0, len(r.known))
	for k := range r.known {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalize lowercases the locale and reduces "en-US" → "en".
func normalize(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if dash := strings.IndexByte(locale, '-'); dash > 0 {
		locale = locale[:dash]
	}
	return locale
}
