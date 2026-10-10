package onboarding

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
)

// Check results.
const (
	CheckPass = "pass"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// Check keys, in display order.
var checkKeys = []string{"email_confirmed", "complete", "consents", "country_allowed", "tax_id_format",
	"disposable_email", "duplicate", "payment_provider"}

var (
	vatPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{2,12}$`)
	icoPattern = regexp.MustCompile(`^[0-9]{6,10}$`)
	einPattern = regexp.MustCompile(`^[0-9]{2}-?[0-9]{7}$`)
)

// disposableDomains is a short list of throw-away mail providers. It only
// raises a warning for the operator, never blocks an applicant.
var disposableDomains = map[string]bool{
	"mailinator.com": true, "guerrillamail.com": true, "10minutemail.com": true, "tempmail.com": true,
	"temp-mail.org": true, "yopmail.com": true, "trashmail.com": true, "sharklasers.com": true,
	"getnada.com": true, "dispostable.com": true, "maildrop.cc": true, "throwawaymail.com": true,
	"fakeinbox.com": true, "mohmal.com": true, "emailondeck.com": true,
}

// WouldApprove is true when every check passed: the answer to "would the
// system approve this by itself?".
func WouldApprove(checks []Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if c.Result != CheckPass {
			return false
		}
	}
	return true
}

// normalizeTaxID uppercases a tax number and drops spaces and punctuation.
func normalizeTaxID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func taxIDFormat(scheme, taxID string) (string, string) {
	n := normalizeTaxID(taxID)
	switch scheme {
	case "vat":
		if !vatPattern.MatchString(n) {
			return CheckWarn, "does not look like a VAT number (two-letter country prefix and digits), so approval will file it as free text (type: other)"
		}
	case "ico":
		if !icoPattern.MatchString(n) {
			return CheckWarn, "a company ID is expected to be 6-10 digits"
		}
	case "ein":
		if !einPattern.MatchString(taxID) && !einPattern.MatchString(n) {
			return CheckWarn, "an EIN is expected as NN-NNNNNNN, so approval will file it as free text (type: other)"
		}
	case "other":
		return CheckPass, "free-form number, not checked"
	}
	return CheckPass, ""
}

// runChecks recomputes the automatic checks of an application and stores them.
func (s *Service) runChecks(ctx context.Context, q querier, app *Application, set Settings) ([]Check, error) {
	a := app.Answers
	var out []Check
	add := func(key, result, detail string) { out = append(out, Check{Key: key, Result: result, Detail: detail}) }

	if app.EmailConfirmedAt != nil {
		add("email_confirmed", CheckPass, "")
	} else {
		add("email_confirmed", CheckFail, "the applicant has not opened the confirmation link")
	}
	if missing := Missing(a); len(missing) == 0 {
		add("complete", CheckPass, "")
	} else {
		add("complete", CheckFail, "missing: "+strings.Join(missing, ", "))
	}
	if a.Bool("accept_terms") && a.Bool("accept_privacy") && a.Bool("confirm_authority") {
		add("consents", CheckPass, "")
	} else {
		add("consents", CheckFail, "terms, privacy and authority must all be accepted")
	}
	if c := a.String("country"); c == "" {
		add("country_allowed", CheckFail, "no country")
	} else if set.allowedCountry(c) {
		add("country_allowed", CheckPass, c)
	} else {
		add("country_allowed", CheckFail, c+" is not in the accepted list")
	}
	if a.String("tax_id") == "" {
		add("tax_id_format", CheckFail, "no tax number")
	} else {
		r, d := taxIDFormat(a.String("tax_id_scheme"), a.String("tax_id"))
		add("tax_id_format", r, d)
	}
	if at := strings.LastIndex(app.Email, "@"); at > 0 && disposableDomains[app.Email[at+1:]] {
		add("disposable_email", CheckWarn, "throw-away mail provider")
	} else {
		add("disposable_email", CheckPass, "")
	}
	dups, err := s.findDuplicates(ctx, q, app)
	if err != nil {
		return nil, err
	}
	if len(dups) == 0 {
		add("duplicate", CheckPass, "")
	} else {
		add("duplicate", CheckWarn, strings.Join(dups, "; "))
	}
	// A provider is "supported" when the module registry has a module for it
	// that renders a hosted payment page (PAY-02), never by a hand-kept list.
	switch choice := a.String("payment_provider"); {
	case choice == "":
		add("payment_provider", CheckFail, "not chosen")
	case providerAccepted(choice):
		add("payment_provider", CheckPass, choice)
	default:
		add("payment_provider", CheckWarn, choice+": not supported yet")
	}

	for i := range out {
		if _, err := q.Exec(ctx, `
INSERT INTO onboarding_application_checks (application_id, key, result, detail, checked_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (application_id, key) DO UPDATE SET result = EXCLUDED.result, detail = EXCLUDED.detail, checked_at = now()`,
			app.ID, out[i].Key, out[i].Result, out[i].Detail); err != nil {
			return nil, fmt.Errorf("onboarding: store check %s: %w", out[i].Key, err)
		}
		out[i].CheckedAt = s.now()
	}
	return out, nil
}

// findDuplicates looks for the same tax number, phone or website host in
// another application or an existing organization.
func (s *Service) findDuplicates(ctx context.Context, q querier, app *Application) ([]string, error) {
	var out []string
	taxID := normalizeTaxID(app.Answers.String("tax_id"))
	phone := app.Answers.String("phone")
	host := websiteHost(app.Answers.String("website"))

	if taxID != "" {
		var n int
		if err := q.QueryRow(ctx, `
SELECT count(*) FROM onboarding_applications
WHERE id <> $1 AND purged_at IS NULL AND status NOT IN ('rejected', 'expired')
  AND upper(regexp_replace(coalesce(answers->>'tax_id', ''), '[^A-Za-z0-9]', '', 'g')) = $2`, app.ID, taxID).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("tax number is in %d other application(s)", n))
		}
		if err := q.QueryRow(ctx, `
SELECT count(*) FROM organizations
WHERE deleted_at IS NULL AND upper(regexp_replace(coalesce(tax_id, ''), '[^A-Za-z0-9]', '', 'g')) = $1`, taxID).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("tax number belongs to %d existing organization(s)", n))
		}
	}
	if phone != "" {
		var n int
		if err := q.QueryRow(ctx, `
SELECT count(*) FROM onboarding_applications
WHERE id <> $1 AND purged_at IS NULL AND status NOT IN ('rejected', 'expired') AND applicant_phone = $2`, app.ID, phone).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("phone is in %d other application(s)", n))
		}
	}
	if name := strings.TrimSpace(app.Answers.String("org_name")); name != "" {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM organizations WHERE deleted_at IS NULL AND lower(name) = lower($1)`, name).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, "an organization with this name already exists (approval would fail)")
		}
	}
	if host != "" {
		var n int
		if err := q.QueryRow(ctx, `
SELECT count(*) FROM onboarding_applications
WHERE id <> $1 AND purged_at IS NULL AND status NOT IN ('rejected', 'expired')
  AND lower(regexp_replace(coalesce(answers->>'website', ''), '^https?://(www\.)?([^/]+).*$', '\2')) = $2`, app.ID, host).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("website is in %d other application(s)", n))
		}
	}
	return out, nil
}

func websiteHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// Checks returns the stored checks of an application in display order.
func (s *Service) Checks(ctx context.Context, id uuid.UUID) ([]Check, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, result, detail, checked_at FROM onboarding_application_checks WHERE application_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]Check{}
	for rows.Next() {
		var c Check
		if err := rows.Scan(&c.Key, &c.Result, &c.Detail, &c.CheckedAt); err != nil {
			return nil, err
		}
		byKey[c.Key] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Check, 0, len(byKey))
	for _, k := range checkKeys {
		if c, ok := byKey[k]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// providerAccepted reports whether the applicant's provider choice is one a
// new workspace's channel starts on as is.
func providerAccepted(choice string) bool {
	_, ok := paymodules.ChannelProviderForChoice(choice)
	return ok
}
