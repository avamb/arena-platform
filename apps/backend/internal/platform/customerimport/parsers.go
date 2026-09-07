// parsers.go — the two source parsers feature #519 ships: a fixed mapper
// for the Bil24 orders export (bil24_orders_json) and a generic CSV parser
// driven by a caller-supplied column mapping. Both produce []ParsedRow,
// the common shape job.go's RunImport feeds into customers.Resolve.
//
// Mapping shape (spec §12.4 names the concept — "mapping jsonb" on
// customer_imports — but leaves the exact JSON to the implementation
// since no HTTP endpoint/consumer exists yet outside this job; feature
// #520 will build the create-import endpoint against this shape):
//
//		{
//		  "frontends": {"https://www.einatwinery.com/": "3e7c...-org-uuid"},
//		  "columns":   {"email": "Email", "phone": "Phone", "name": "Full Name"},
//		  "org_rule":  {"column": "Org"}
//		}
//
//	  - frontends maps a Bil24 order's frontend.name (a URL string identifying
//	    the WP site / sales channel) to the target organization UUID. A
//	    frontend.name with no entry is kept as the row's OrgKey verbatim (the
//	    job then fails to resolve an org for it and marks the row skipped —
//	    see job.go) rather than silently guessing.
//	  - columns maps a target ParsedRow field name (email, phone, name,
//	    tickets_count) to the CSV header that carries it. Only email/phone/
//	    name/tickets_count are recognised; any other target key is ignored.
//	  - org_rule.column names a CSV column whose value becomes the row's
//	    OrgKey directly (the frontends map is reused to resolve it to a
//	    UUID); org_rule.org_id, when set, pins every row in the file to one
//	    fixed org UUID string and skips the column lookup entirely. Exactly
//	    one of the two should be set — org_id takes precedence if both are.
package customerimport

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Mapping is the decoded form of customer_imports.mapping (jsonb). See the
// package doc comment above for field semantics.
type Mapping struct {
	Frontends map[string]uuid.UUID `json:"frontends"`
	Columns   map[string]string    `json:"columns"`
	OrgRule   OrgRule              `json:"org_rule"`
}

// OrgRule configures how ParseCSVRows derives each row's OrgKey.
type OrgRule struct {
	// Column names the CSV header whose value becomes OrgKey.
	Column string `json:"column"`
	// OrgID, when non-empty, pins every row to this org and disables the
	// per-row Column lookup.
	OrgID string `json:"org_id"`
}

// ParsedRow is the source-agnostic shape both parsers emit; job.go feeds
// it straight into customers.Resolve without caring which parser produced
// it.
type ParsedRow struct {
	// RawJSON is the canonical bytes job.go hashes for row_hash and stores
	// verbatim in customer_import_rows.raw. For the JSON parser this is the
	// re-marshalled order object; for the CSV parser it is a small JSON
	// object built from the raw CSV record.
	RawJSON []byte

	Email string
	Phone string
	Name  string

	// OrgKey identifies the row's organization before mapping.Frontends
	// resolves it to a uuid.UUID. Empty when the import is single-org
	// (customer_imports.org_id is set) — job.go only consults OrgKey when
	// the import's org_id column is NULL.
	OrgKey string

	FirstOrderAt *time.Time
	LastOrderAt  *time.Time
	TicketsCount int

	// Interests aggregates distinct non-empty actionEvent.actionName
	// values (Bil24) — spec's stand-in for "what did this customer show
	// interest in".
	Interests []string
	// PromoCodesUsed aggregates distinct non-empty discountReason values.
	PromoCodesUsed []string
}

// ─────────────────────────────────────────────────────────────────────────
// bil24_orders_json
// ─────────────────────────────────────────────────────────────────────────

type bil24Order struct {
	ID   int64  `json:"id"`
	Date string `json:"date"`
	User struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
	Frontend struct {
		Name string `json:"name"`
	} `json:"frontend"`
	TicketList     []bil24Ticket `json:"ticketList"`
	TicketQuantity int           `json:"ticketQuantity"`
	Email          *string       `json:"email"`
	Phone          string        `json:"phone"`
	FullName       string        `json:"fullName"`
}

type bil24Ticket struct {
	DiscountReason string `json:"discountReason"`
	ActionEvent    struct {
		ActionName string `json:"actionName"`
	} `json:"actionEvent"`
}

// ParseBil24OrdersJSON parses the fixed Bil24 order-export shape: a JSON
// array of order objects (see
// tests/compat/bil24/testdata/wp/bil24_orders_pseudonymized.json). One
// ParsedRow is produced per order. discountReason and actionEvent.
// actionName live on each order's ticketList[] entries (per-ticket, not
// per-order) — this parser aggregates the distinct non-empty values across
// an order's tickets into PromoCodesUsed / Interests respectively.
//
// Email/Phone/Name prefer the order's top-level email/phone/fullName
// (the fields the WP checkout actually collected) and fall back to
// user.email when the top-level email is absent — the fixture's
// user.email is consistently "" while the top-level fields carry the real
// pseudonymized contact data.
func ParseBil24OrdersJSON(data []byte, mapping Mapping) ([]ParsedRow, error) {
	var orders []bil24Order
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&orders); err != nil {
		return nil, fmt.Errorf("customerimport: parse bil24_orders_json: %w", err)
	}

	out := make([]ParsedRow, 0, len(orders))
	for i, o := range orders {
		email := o.User.Email
		if o.Email != nil && strings.TrimSpace(*o.Email) != "" {
			email = *o.Email
		}

		var orderAt *time.Time
		if t, ok := parseBil24Date(o.Date); ok {
			orderAt = &t
		}

		interests := dedupeNonEmpty(func(yield func(string)) {
			for _, tk := range o.TicketList {
				yield(tk.ActionEvent.ActionName)
			}
		})
		promoCodes := dedupeNonEmpty(func(yield func(string)) {
			for _, tk := range o.TicketList {
				yield(tk.DiscountReason)
			}
		})

		tickets := o.TicketQuantity
		if tickets == 0 {
			tickets = len(o.TicketList)
		}

		orgKey := o.Frontend.Name
		if resolved, ok := mapping.Frontends[o.Frontend.Name]; ok {
			orgKey = resolved.String()
		}

		raw, err := json.Marshal(o)
		if err != nil {
			return nil, fmt.Errorf("customerimport: re-marshal bil24 order %d (index %d): %w", o.ID, i, err)
		}

		out = append(out, ParsedRow{
			RawJSON:        raw,
			Email:          strings.TrimSpace(email),
			Phone:          strings.TrimSpace(o.Phone),
			Name:           strings.TrimSpace(o.FullName),
			OrgKey:         orgKey,
			FirstOrderAt:   orderAt,
			LastOrderAt:    orderAt,
			TicketsCount:   tickets,
			Interests:      interests,
			PromoCodesUsed: promoCodes,
		})
	}
	return out, nil
}

// parseBil24Date parses the order.date field. The fixture carries RFC3339
// with a milliseconds fraction and a numeric zone offset
// ("2026-02-13T09:36:50.008+01:00"), which time.RFC3339 parses correctly
// (Go's time.Parse special-cases a fractional-second suffix for the
// RFC3339 layout even though the layout string itself has none). A date
// that fails to parse is not fatal — the row still resolves through
// customers.Resolve, just without order timestamps for the org-link
// counters (job.go falls back to the run's Now()).
func parseBil24Date(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// dedupeNonEmpty walks a small internal-iterator-style yield callback (kept
// local rather than pulling in an iterator package for two call sites) and
// returns the distinct non-empty values it saw, in first-seen order.
func dedupeNonEmpty(walk func(yield func(string))) []string {
	seen := make(map[string]struct{})
	var out []string
	walk(func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	})
	return out
}

// ─────────────────────────────────────────────────────────────────────────
// generic CSV (mapping.columns / mapping.org_rule)
// ─────────────────────────────────────────────────────────────────────────

// ParseCSVRows parses a generic CSV export using mapping.Columns to locate
// the email/phone/name/tickets_count fields by header name and
// mapping.OrgRule to derive each row's OrgKey. The first record is always
// treated as the header row.
func ParseCSVRows(data []byte, mapping Mapping) ([]ParsedRow, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1 // tolerate ragged trailing rows rather than erroring the whole file
	header, err := r.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("customerimport: CSV file has no header row")
		}
		return nil, fmt.Errorf("customerimport: read CSV header: %w", err)
	}

	colIdx := make(map[string]int, len(header))
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	lookup := func(target string) (int, bool) {
		name, ok := mapping.Columns[target]
		if !ok || name == "" {
			return 0, false
		}
		idx, ok := colIdx[name]
		return idx, ok
	}

	emailIdx, hasEmail := lookup("email")
	phoneIdx, hasPhone := lookup("phone")
	nameIdx, hasName := lookup("name")
	ticketsIdx, hasTickets := lookup("tickets_count")

	var orgColIdx int
	hasOrgCol := false
	if mapping.OrgRule.OrgID == "" && mapping.OrgRule.Column != "" {
		orgColIdx, hasOrgCol = colIdx[mapping.OrgRule.Column]
	}

	get := func(rec []string, idx int) string {
		if idx < 0 || idx >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[idx])
	}

	var out []ParsedRow
	rowNo := 0
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("customerimport: read CSV row %d: %w", rowNo+1, err)
		}
		rowNo++

		orgKey := mapping.OrgRule.OrgID
		if orgKey == "" && hasOrgCol {
			key := get(rec, orgColIdx)
			if resolved, ok := mapping.Frontends[key]; ok {
				orgKey = resolved.String()
			} else {
				orgKey = key
			}
		}

		tickets := 0
		if hasTickets {
			if n, err := strconv.Atoi(get(rec, ticketsIdx)); err == nil {
				tickets = n
			}
		}

		rawObj := map[string]string{}
		for i, h := range header {
			rawObj[h] = get(rec, i)
		}
		raw, err := json.Marshal(rawObj)
		if err != nil {
			return nil, fmt.Errorf("customerimport: marshal CSV row %d: %w", rowNo, err)
		}

		row := ParsedRow{
			RawJSON:      raw,
			OrgKey:       orgKey,
			TicketsCount: tickets,
		}
		if hasEmail {
			row.Email = get(rec, emailIdx)
		}
		if hasPhone {
			row.Phone = get(rec, phoneIdx)
		}
		if hasName {
			row.Name = get(rec, nameIdx)
		}
		out = append(out, row)
	}
	return out, nil
}
