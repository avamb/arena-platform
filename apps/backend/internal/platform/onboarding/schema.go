// Package onboarding implements the organizer application flow
// (08_architecture/34_onboarding_applications_ru.md): a draft the applicant can
// leave and come back to, the operator's queue, and the approval that turns an
// application into a workspace.
//
// schema.go is the single description of the form. The website, the Telegram
// bot and the server-side validation all read it: a client never hard-codes a
// field, a range or a list of options, and a new field is one entry here (the
// answers live in a jsonb column, so no migration is needed).
package onboarding

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// SchemaVersion is bumped when a field is removed or changes meaning.
const SchemaVersion = 1

// Kind is the input type of a field.
type Kind string

// Field kinds understood by validation and by the clients.
const (
	KindText        Kind = "text"
	KindTextarea    Kind = "textarea"
	KindEmail       Kind = "email"
	KindPhone       Kind = "phone"
	KindCountry     Kind = "country"
	KindSelect      Kind = "select"
	KindMultiselect Kind = "multiselect"
	KindBool        Kind = "bool"
	KindURL         Kind = "url"
	KindURLList     Kind = "url_list"
	KindDate        Kind = "date"
	KindNumber      Kind = "number"
	KindCurrency    Kind = "currency"
)

// Steps in the order the applicant sees them.
const (
	StepContact      = "contact"
	StepOrganization = "organization"
	StepEvents       = "events"
	StepPlatform     = "platform"
	StepConsents     = "consents"
	// StepReview is not a form step: current_step reports it once every
	// required field is filled.
	StepReview = "review"
)

// Steps lists the form steps in order.
var Steps = []string{StepContact, StepOrganization, StepEvents, StepPlatform, StepConsents}

// Field describes one answer.
type Field struct {
	Key      string
	Step     string
	Kind     Kind
	Required bool
	// MaxLen bounds text kinds (runes); 0 uses defaultTextMax.
	MaxLen  int
	Options []string
	// ReadOnly fields are set by the server (the e-mail confirmed by link).
	ReadOnly bool
	// DefaultFrom names another field whose value fills this one at submit
	// when it was left empty (the registered address defaults to the country).
	DefaultFrom string
}

const (
	defaultTextMax = 200
	maxListItems   = 5
)

// Option lists the machine values clients render as choices.
var (
	taxSchemes     = []string{"vat", "ico", "ein", "other"}
	eventTypes     = []string{"concert", "theatre", "masterclass", "festival", "sport", "tour", "other"}
	seatingKinds   = []string{"ga", "seated", "both"}
	eventsPerYear  = []string{"1-5", "6-20", "21-100", "100+"}
	ticketsPerYear = []string{"<500", "500-5k", "5k-50k", "50k+"}
	previousSystem = []string{"bil24", "other", "none"}
	websiteKinds   = []string{"wordpress", "other", "none"}
	providers      = []string{"stripe", "flitt", "other", "undecided"}
)

// Fields is the form, in display order.
var Fields = []Field{
	{Key: "first_name", Step: StepContact, Kind: KindText, Required: true, MaxLen: 80},
	{Key: "last_name", Step: StepContact, Kind: KindText, Required: true, MaxLen: 80},
	{Key: "email", Step: StepContact, Kind: KindEmail, Required: true, ReadOnly: true},
	{Key: "phone", Step: StepContact, Kind: KindPhone, Required: true},
	{Key: "telegram_username", Step: StepContact, Kind: KindText, MaxLen: 32},

	{Key: "org_name", Step: StepOrganization, Kind: KindText, Required: true, MaxLen: 120},
	{Key: "legal_name", Step: StepOrganization, Kind: KindText, Required: true, MaxLen: 200},
	{Key: "country", Step: StepOrganization, Kind: KindCountry, Required: true},
	{Key: "legal_form", Step: StepOrganization, Kind: KindText, MaxLen: 80},
	{Key: "tax_id", Step: StepOrganization, Kind: KindText, Required: true, MaxLen: 40},
	{Key: "tax_id_scheme", Step: StepOrganization, Kind: KindSelect, Required: true, Options: taxSchemes},
	{Key: "registration_number", Step: StepOrganization, Kind: KindText, MaxLen: 60},
	{Key: "address_line1", Step: StepOrganization, Kind: KindText, Required: true, MaxLen: 200},
	{Key: "address_postal_code", Step: StepOrganization, Kind: KindText, Required: true, MaxLen: 20},
	{Key: "address_city", Step: StepOrganization, Kind: KindText, Required: true, MaxLen: 100},
	{Key: "address_country", Step: StepOrganization, Kind: KindCountry, Required: true, DefaultFrom: "country"},
	{Key: "website", Step: StepOrganization, Kind: KindURL},
	{Key: "social_links", Step: StepOrganization, Kind: KindURLList},

	{Key: "event_types", Step: StepEvents, Kind: KindMultiselect, Required: true, Options: eventTypes},
	{Key: "seating", Step: StepEvents, Kind: KindSelect, Required: true, Options: seatingKinds},
	{Key: "events_per_year", Step: StepEvents, Kind: KindSelect, Required: true, Options: eventsPerYear},
	{Key: "tickets_per_year", Step: StepEvents, Kind: KindSelect, Required: true, Options: ticketsPerYear},
	{Key: "avg_ticket_price", Step: StepEvents, Kind: KindNumber},
	{Key: "currency", Step: StepEvents, Kind: KindCurrency},
	{Key: "first_event_date", Step: StepEvents, Kind: KindDate},

	{Key: "previous_system", Step: StepPlatform, Kind: KindSelect, Options: previousSystem},
	{Key: "previous_system_name", Step: StepPlatform, Kind: KindText, MaxLen: 100},
	{Key: "has_website", Step: StepPlatform, Kind: KindBool},
	{Key: "website_platform", Step: StepPlatform, Kind: KindSelect, Options: websiteKinds},
	{Key: "wants_wp_plugin", Step: StepPlatform, Kind: KindBool},
	{Key: "payment_provider", Step: StepPlatform, Kind: KindSelect, Required: true, Options: providers},
	{Key: "has_payment_account", Step: StepPlatform, Kind: KindBool},
	{Key: "notes", Step: StepPlatform, Kind: KindTextarea, MaxLen: 2000},

	{Key: "accept_terms", Step: StepConsents, Kind: KindBool, Required: true},
	{Key: "accept_privacy", Step: StepConsents, Kind: KindBool, Required: true},
	{Key: "confirm_authority", Step: StepConsents, Kind: KindBool, Required: true},
	{Key: "marketing_opt_in", Step: StepConsents, Kind: KindBool},
}

var fieldIndex = func() map[string]Field {
	m := make(map[string]Field, len(Fields))
	for _, f := range Fields {
		m[f.Key] = f
	}
	return m
}()

// LookupField returns the definition of key.
func LookupField(key string) (Field, bool) {
	f, ok := fieldIndex[key]
	return f, ok
}

// consentKeys must all be true to submit.
var consentKeys = map[string]bool{"accept_terms": true, "accept_privacy": true, "confirm_authority": true}

var (
	phonePattern    = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	tgPattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)
)

// FieldErrors maps a field key to a machine reason: required, invalid,
// too_long, not_allowed, read_only, unknown_field.
type FieldErrors map[string]string

// NormalizePhone strips punctuation and returns the E.164 form, or "" when the
// number is not one.
func NormalizePhone(raw string) string {
	var b strings.Builder
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return ""
		}
	}
	s := b.String()
	if !phonePattern.MatchString(s) {
		return ""
	}
	return s
}

// NormalizeValue validates one answer and returns its stored form: a string,
// a bool or a []string. A nil or empty value means "clear" and returns
// (nil, "").
func NormalizeValue(f Field, v any) (any, string) {
	if isEmpty(v) {
		return nil, ""
	}
	switch f.Kind {
	case KindBool:
		b, ok := v.(bool)
		if !ok {
			return nil, "invalid"
		}
		return b, ""
	case KindMultiselect:
		list, ok := toStringList(v)
		if !ok {
			return nil, "invalid"
		}
		seen := map[string]bool{}
		out := make([]string, 0, len(list))
		for _, item := range list {
			item = strings.TrimSpace(item)
			if !contains(f.Options, item) {
				return nil, "not_allowed"
			}
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
		if len(out) == 0 {
			return nil, ""
		}
		return out, ""
	case KindURLList:
		list, ok := toStringList(v)
		if !ok {
			return nil, "invalid"
		}
		if len(list) > maxListItems {
			return nil, "too_long"
		}
		out := make([]string, 0, len(list))
		for _, item := range list {
			u, reason := normalizeURL(item)
			if reason != "" {
				return nil, reason
			}
			if u != "" {
				out = append(out, u)
			}
		}
		if len(out) == 0 {
			return nil, ""
		}
		return out, ""
	case KindNumber:
		n, ok := toFloat(v)
		if !ok || n < 0 || n > 1_000_000 {
			return nil, "invalid"
		}
		return trimFloat(n), ""
	}

	s, ok := v.(string)
	if !ok {
		return nil, "invalid"
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ""
	}
	if f.Kind != KindTextarea {
		s = strings.Join(strings.Fields(s), " ")
	}
	limit := f.MaxLen
	if limit == 0 {
		limit = defaultTextMax
	}
	if utf8.RuneCountInString(s) > limit {
		return nil, "too_long"
	}
	switch f.Kind {
	case KindEmail:
		addr, err := mail.ParseAddress(s)
		if err != nil || addr.Address != s || !strings.Contains(s[strings.LastIndex(s, "@"):], ".") {
			return nil, "invalid"
		}
		return strings.ToLower(s), ""
	case KindPhone:
		p := NormalizePhone(s)
		if p == "" {
			return nil, "invalid"
		}
		return p, ""
	case KindCountry:
		c := strings.ToUpper(s)
		if !countryPattern.MatchString(c) {
			return nil, "invalid"
		}
		return c, ""
	case KindCurrency:
		c := strings.ToUpper(s)
		if !currencyPattern.MatchString(c) {
			return nil, "invalid"
		}
		return c, ""
	case KindSelect:
		if !contains(f.Options, s) {
			return nil, "not_allowed"
		}
		return s, ""
	case KindURL:
		u, reason := normalizeURL(s)
		if reason != "" {
			return nil, reason
		}
		return u, ""
	case KindDate:
		if _, err := time.Parse("2006-01-02", s); err != nil { // allow:timeformat
			return nil, "invalid"
		}
		return s, ""
	}
	if f.Key == "telegram_username" {
		s = strings.TrimPrefix(s, "@")
		if !tgPattern.MatchString(s) {
			return nil, "invalid"
		}
	}
	return s, ""
}

// normalizeURL accepts http(s) URLs and adds https:// to a bare domain.
func normalizeURL(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !strings.Contains(u.Host, ".") {
		return "", "invalid"
	}
	if utf8.RuneCountInString(raw) > 300 {
		return "", "too_long"
	}
	return u.String(), ""
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case []string:
		return len(x) == 0
	}
	return false
}

func toStringList(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%g", &f); err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// trimFloat keeps at most two decimals, as a price has.
func trimFloat(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Answers is the stored answer set.
type Answers map[string]any

// String returns a string answer or "".
func (a Answers) String(key string) string {
	s, _ := a[key].(string)
	return s
}

// Bool returns a bool answer.
func (a Answers) Bool(key string) bool {
	b, _ := a[key].(bool)
	return b
}

// List returns a list answer.
func (a Answers) List(key string) []string {
	switch x := a[key].(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// filled reports whether key holds a usable answer.
func (a Answers) filled(f Field) bool {
	v, ok := a[f.Key]
	if !ok || isEmpty(v) {
		return false
	}
	if consentKeys[f.Key] {
		b, _ := v.(bool)
		return b
	}
	return true
}

// Missing lists the required fields that are still empty. A field with a
// DefaultFrom counts as filled when its source is.
func Missing(a Answers) []string {
	var out []string
	for _, f := range Fields {
		if !f.Required || f.ReadOnly && a.filled(f) {
			continue
		}
		if a.filled(f) {
			continue
		}
		if f.DefaultFrom != "" {
			if src, ok := LookupField(f.DefaultFrom); ok && a.filled(src) {
				continue
			}
		}
		out = append(out, f.Key)
	}
	return out
}

// Progress returns the share of required fields filled (0-100) and the step
// the applicant should be sent to: the first step with a gap, or "review".
func Progress(a Answers) (pct int, step string) {
	total, done := 0, 0
	missing := map[string]bool{}
	for _, k := range Missing(a) {
		missing[k] = true
	}
	for _, f := range Fields {
		if !f.Required {
			continue
		}
		total++
		if !missing[f.Key] {
			done++
		}
	}
	step = StepReview
	for _, s := range Steps {
		for _, f := range Fields {
			if f.Step == s && missing[f.Key] {
				step = s
				break
			}
		}
		if step != StepReview {
			break
		}
	}
	if total == 0 {
		return 100, step
	}
	return done * 100 / total, step
}

// ApplyDefaults fills the DefaultFrom fields that are empty.
func ApplyDefaults(a Answers) {
	for _, f := range Fields {
		if f.DefaultFrom == "" || a.filled(f) {
			continue
		}
		if v, ok := a[f.DefaultFrom]; ok && !isEmpty(v) {
			a[f.Key] = v
		}
	}
}

// ValidatePatch normalizes a partial update. Nothing is applied unless every
// key is valid; the returned map is what to merge (a nil value deletes).
func ValidatePatch(patch map[string]any, allowed func(key string) bool) (map[string]any, FieldErrors) {
	errs := FieldErrors{}
	out := make(map[string]any, len(patch))
	for key, raw := range patch {
		f, ok := LookupField(key)
		if !ok {
			errs[key] = "unknown_field"
			continue
		}
		if f.ReadOnly {
			errs[key] = "read_only"
			continue
		}
		if allowed != nil && !allowed(key) {
			errs[key] = "locked"
			continue
		}
		v, reason := NormalizeValue(f, raw)
		if reason != "" {
			errs[key] = reason
			continue
		}
		out[key] = v
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}
