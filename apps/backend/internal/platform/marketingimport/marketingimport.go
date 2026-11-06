// Package marketingimport is the whole logic of cmd/arena-marketing-import
// (spec 62 section 10.1, P-18): it reads the two exports of the Vino&Co
// mailing base, merges them by normalized e-mail and writes the result into
// marketing_subscriptions / marketing_consents through the marketing package.
//
// The two files:
//
//   - a Brevo contacts export (CSV): email, name (or firstname + lastname),
//     blacklisted, unsubscribed, consent_date, consent_source;
//   - the Elementor "club friends" form export (CSV): date, name, email, phone.
//
// The rules (owner decision, spec 62 10.1):
//
//  1. Unsubscribed and blacklisted contacts of Brevo are imported as
//     SUPPRESSIONS and win over everything else, in this run and in the
//     database: an address that is suppressed is never made a subscriber.
//  2. A Brevo contact becomes a subscriber only with BOTH a consent date and a
//     consent source; a form application of Elementor always carries its own
//     date (the date of the application is the date of the consent).
//  3. Without consent data a contact is skipped and counted.
//  4. Addresses are deduplicated by normalized e-mail; of several consents the
//     EARLIEST wins. The import is idempotent.
package marketingimport

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

// Kind of a parsed record.
type Kind string

// Kinds.
const (
	KindSuppress  Kind = "suppress"
	KindSubscribe Kind = "subscribe"
	// KindNoConsent: a contact the file lists without consent data.
	KindNoConsent Kind = "no_consent"
)

// Record is one parsed line of either file.
type Record struct {
	File          string // "brevo" or "elementor"
	Line          int
	Email         string
	Name          string
	Kind          Kind
	ConsentAt     time.Time
	ConsentSource string
	Source        string // marketing.SourceImportBrevo / SourceImportElementor
}

// Counts is the report of a run, per category.
type Counts struct {
	BrevoRows     int
	ElementorRows int
	InvalidEmail  int
	// InvalidDate: a consent date that could not be read (the contact is
	// treated as having no consent data).
	InvalidDate int
	// DuplicatesMerged: records folded into another with the same address.
	DuplicatesMerged int
	// NoConsent: contacts skipped for lack of consent date or source.
	NoConsent int

	SuppressionsNew       int
	SuppressionsApplied   int
	SuppressionsUnchanged int

	SubscribersNew        int
	SubscribersPresent    int
	SubscribersSuppressed int
}

// Problem is a line that could not be used, for the printed report.
type Problem struct {
	File   string
	Line   int
	Email  string
	Reason string
}

// ParseBrevo reads a Brevo contacts export.
func ParseBrevo(r io.Reader) ([]Record, []Problem, error) {
	tbl, err := readTable(r, []string{"email"}, map[string][]string{
		"email":          {"email", "email_address", "e-mail"},
		"name":           {"name", "full_name"},
		"firstname":      {"firstname", "first_name"},
		"lastname":       {"lastname", "last_name"},
		"blacklisted":    {"blacklisted", "emailblacklisted", "email_blacklisted"},
		"unsubscribed":   {"unsubscribed", "unsubscribe", "unsubscribed_at"},
		"consent_date":   {"consent_date", "consent_at", "opt_in_date", "optin_date"},
		"consent_source": {"consent_source", "opt_in_source", "optin_source", "source"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("brevo file: %w", err)
	}
	var out []Record
	var probs []Problem
	for _, row := range tbl.rows {
		rec := Record{File: "brevo", Line: row.line, Email: row.get("email"), Source: marketing.SourceImportBrevo}
		rec.Name = strings.TrimSpace(row.get("name"))
		if rec.Name == "" {
			rec.Name = strings.TrimSpace(row.get("firstname") + " " + row.get("lastname"))
		}
		if truthy(row.get("blacklisted")) || truthy(row.get("unsubscribed")) {
			rec.Kind = KindSuppress
			out = append(out, rec)
			continue
		}
		rec.ConsentSource = strings.TrimSpace(row.get("consent_source"))
		if ds := row.get("consent_date"); ds != "" {
			at, ok := parseDate(ds)
			if !ok {
				probs = append(probs, Problem{File: "brevo", Line: row.line, Email: rec.Email, Reason: "consent date not readable: " + ds})
				rec.Kind = KindNoConsent
				rec.ConsentSource = "invalid_date"
				out = append(out, rec)
				continue
			}
			rec.ConsentAt = at
		}
		if rec.ConsentAt.IsZero() || rec.ConsentSource == "" {
			rec.Kind = KindNoConsent
		} else {
			rec.Kind = KindSubscribe
		}
		out = append(out, rec)
	}
	return out, probs, nil
}

// ParseElementor reads the club-form export (date, name, email, phone). The
// phone is read but not stored: the subscription keeps no phone number.
func ParseElementor(r io.Reader) ([]Record, []Problem, error) {
	tbl, err := readTable(r, []string{"email", "date"}, map[string][]string{
		"email": {"email", "e-mail", "email_address"},
		"date":  {"date", "created_at", "submitted_at", "date_created"},
		"name":  {"name", "full_name"},
		"phone": {"phone", "tel", "telephone"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("elementor file: %w", err)
	}
	var out []Record
	var probs []Problem
	for _, row := range tbl.rows {
		rec := Record{File: "elementor", Line: row.line, Email: row.get("email"), Name: strings.TrimSpace(row.get("name")),
			Source: marketing.SourceImportElementor, ConsentSource: "club form"}
		at, ok := parseDate(row.get("date"))
		if !ok {
			probs = append(probs, Problem{File: "elementor", Line: row.line, Email: rec.Email, Reason: "application date not readable: " + row.get("date")})
			rec.Kind = KindNoConsent
		} else {
			rec.ConsentAt = at
			rec.Kind = KindSubscribe
		}
		out = append(out, rec)
	}
	return out, probs, nil
}

// merged is one address after deduplication.
type merged struct {
	email    string // normalized
	original string
	name     string
	suppress *Record
	consent  *Record // the earliest consent
}

// Merge normalizes and deduplicates the records of both files. Invalid
// addresses and records without consent are counted and dropped; the result
// is sorted by address so a run is reproducible.
func Merge(recs []Record, c *Counts) []merged {
	by := map[string]*merged{}
	for i := range recs {
		rec := recs[i]
		switch rec.File {
		case "brevo":
			c.BrevoRows++
		case "elementor":
			c.ElementorRows++
		}
		norm, err := marketing.NormalizeEmail(rec.Email)
		if err != nil {
			c.InvalidEmail++
			continue
		}
		m, seen := by[norm]
		if !seen {
			m = &merged{email: norm, original: strings.TrimSpace(rec.Email)}
			by[norm] = m
		} else {
			c.DuplicatesMerged++
		}
		if m.name == "" {
			m.name = rec.Name
		}
		switch rec.Kind {
		case KindSuppress:
			if m.suppress == nil {
				m.suppress = &recs[i]
			}
		case KindSubscribe:
			if m.consent == nil || rec.ConsentAt.Before(m.consent.ConsentAt) {
				m.consent = &recs[i]
			}
		}
	}
	out := make([]merged, 0, len(by))
	for _, m := range by {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].email < out[j].email })
	return out
}

// TxBeginner is a pool.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Run merges the records and writes them in ONE transaction; with dryRun the
// transaction is rolled back, so the counts are exactly what a real run would
// write. now stamps the import (zero: the clock).
func Run(ctx context.Context, db TxBeginner, orgID uuid.UUID, recs []Record, dryRun bool, now time.Time) (Counts, error) {
	var c Counts
	list := Merge(recs, &c)
	tx, err := db.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Suppressions first: they must exist before any subscriber is judged.
	for _, m := range list {
		if m.suppress == nil {
			continue
		}
		out, err := marketing.ImportSuppressionTx(ctx, tx, marketing.ImportContact{
			OrgID: orgID, EmailNormalized: m.email, Email: m.original, Name: m.name,
			Source: m.suppress.Source, ConsentSource: m.suppress.ConsentSource,
			At: m.suppress.ConsentAt, Now: now,
		})
		if err != nil {
			return c, fmt.Errorf("suppression %s: %w", m.email, err)
		}
		switch out {
		case marketing.ImportSuppressionNew:
			c.SuppressionsNew++
		case marketing.ImportSuppressionApplied:
			c.SuppressionsApplied++
		default:
			c.SuppressionsUnchanged++
		}
	}
	for _, m := range list {
		if m.suppress != nil {
			if m.consent != nil {
				c.SubscribersSuppressed++
			}
			continue
		}
		if m.consent == nil {
			c.NoConsent++
			continue
		}
		out, err := marketing.ImportSubscriberTx(ctx, tx, marketing.ImportContact{
			OrgID: orgID, EmailNormalized: m.email, Email: m.original, Name: m.name,
			Source: m.consent.Source, ConsentSource: m.consent.ConsentSource,
			At: m.consent.ConsentAt, Now: now,
		})
		if err != nil {
			return c, fmt.Errorf("subscriber %s: %w", m.email, err)
		}
		switch out {
		case marketing.ImportSubscriberNew:
			c.SubscribersNew++
		case marketing.ImportSubscriberSuppressed:
			c.SubscribersSuppressed++
		default:
			c.SubscribersPresent++
		}
	}
	if dryRun {
		return c, nil
	}
	return c, tx.Commit(ctx)
}

// ─── CSV helpers ─────────────────────────────────────────────────────────────

type tableRow struct {
	line int
	cols map[string]string
}

func (r tableRow) get(k string) string { return strings.TrimSpace(r.cols[k]) }

type table struct{ rows []tableRow }

// readTable reads a CSV (comma or semicolon, BOM tolerated, header names
// case-insensitive) and maps header aliases onto canonical names. required
// lists canonical names that must be present.
func readTable(r io.Reader, required []string, aliases map[string][]string) (*table, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(raw), string([]byte{0xEF, 0xBB, 0xBF}))
	comma := ','
	if nl := strings.IndexAny(text, "\r\n"); nl > 0 {
		if head := text[:nl]; strings.Count(head, ";") > strings.Count(head, ",") {
			comma = ';'
		}
	}
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("no header line: %w", err)
	}
	pos := map[string]int{}
	for i, h := range head {
		h = strings.ToLower(strings.TrimSpace(h))
		for canon, names := range aliases {
			for _, n := range names {
				if h == n {
					if _, taken := pos[canon]; !taken {
						pos[canon] = i
					}
				}
			}
		}
	}
	for _, req := range required {
		if _, ok := pos[req]; !ok {
			return nil, fmt.Errorf("column %q is missing (accepted names: %s)", req, strings.Join(aliases[req], ", "))
		}
	}
	t := &table{}
	line := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			continue
		}
		empty := true
		cols := map[string]string{}
		for canon, i := range pos {
			if i < len(rec) {
				cols[canon] = rec[i]
				if strings.TrimSpace(rec[i]) != "" {
					empty = false
				}
			}
		}
		if empty {
			continue
		}
		t.rows = append(t.rows, tableRow{line: line, cols: cols})
	}
	return t, nil
}

// truthy reads Brevo's flag spellings: 1, true, yes, y, da, and a non-empty
// date (the "unsubscribed_at" style).
func truthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "0", "false", "no", "n", "нет", "null", "none":
		return false
	}
	return true
}

var dateLayouts = []string{
	time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	"02.01.2006 15:04:05", "02.01.2006 15:04", "02.01.2006", "02/01/2006 15:04:05", "02/01/2006",
	"Jan 2, 2006", "January 2, 2006",
}

// parseDate reads the date spellings the two tools export. A numeric date
// with slashes or dots is day first (the tools export in the site's locale).
func parseDate(s string) (time.Time, bool) {
	s = strings.TrimFunc(strings.TrimSpace(s), func(r rune) bool { return unicode.IsSpace(r) || r == '"' })
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
