package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Outcomes of one entity.
const (
	outCreated   = "created"
	outUpdated   = "updated"
	outUnchanged = "unchanged"
	outSkipped   = "skipped"
	outFailed    = "failed"
)

// Counter tallies one kind of entity. Reasons explain skipped and failed
// ones (and warnings), keyed by a short text.
type Counter struct {
	Created, Updated, Unchanged, Skipped, Failed int
	Reasons                                      map[string]int
}

func (c *Counter) add(outcome, reason string) {
	switch outcome {
	case outCreated:
		c.Created++
	case outUpdated:
		c.Updated++
	case outUnchanged:
		c.Unchanged++
	case outSkipped:
		c.Skipped++
	case outFailed:
		c.Failed++
	}
	if reason != "" {
		if c.Reasons == nil {
			c.Reasons = map[string]int{}
		}
		c.Reasons[reason]++
	}
}

func (c *Counter) total() int { return c.Created + c.Updated + c.Unchanged + c.Skipped + c.Failed }

// Report is what a run did (or, in a dry run, would do). It carries counts,
// product handles and amounts, never a customer's name, e-mail or phone.
type Report struct {
	DryRun   bool
	OrgName  string
	Currency string
	Offline  bool // a dry run without a database: everything counts as new

	Products  Counter
	Variants  Counter
	Images    Counter
	Customers Counter
	Consents  Counter // e-mail marketing consents (customer_consents, kind marketing): Created = new rows
	// ConsentsSMS and ConsentsWhatsApp are the other two marketing channels of
	// the export. customer_consents has no channel, so they live in
	// customer_attributes (see importCustomer).
	ConsentsSMS      Counter
	ConsentsWhatsApp Counter
	Pages            Counter
	Settings         Counter

	SkippedByStatus map[string]int // Shopify status of products left out
	// MissingImages lists "handle: file" of pictures that could not be read.
	MissingImages []string
	// StockValueMinor is the sum of price x stock over the imported variants.
	StockValueMinor int64
	MinPriceMinor   int64
	MaxPriceMinor   int64
	Orders          *OrdersSummary
	// BodyImagesDropped and BodyTablesDropped count pictures and tables the
	// shop's rich-text profile removed from descriptions and pages.
	BodyImagesDropped int
	BodyTablesDropped int
	Problems          []string
	Notes             []string
}

func (r *Report) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

func (r *Report) problem(format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

func (r *Report) notePrice(minor int64) {
	if r.MinPriceMinor == 0 && r.MaxPriceMinor == 0 {
		r.MinPriceMinor, r.MaxPriceMinor = minor, minor
		return
	}
	if minor < r.MinPriceMinor {
		r.MinPriceMinor = minor
	}
	if minor > r.MaxPriceMinor {
		r.MaxPriceMinor = minor
	}
}

func writeCounter(w io.Writer, name string, c *Counter, dry bool) {
	if c.total() == 0 && len(c.Reasons) == 0 {
		return
	}
	verbs := [5]string{"created", "updated", "unchanged", "skipped", "failed"}
	if dry {
		verbs = [5]string{"would be created", "would be updated", "unchanged", "skipped", "would fail"}
	}
	var parts []string
	for i, n := range []int{c.Created, c.Updated, c.Unchanged, c.Skipped, c.Failed} {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, verbs[i]))
		}
	}
	if len(parts) == 0 {
		parts = []string{"nothing to do"}
	}
	fmt.Fprintf(w, "  %-10s %s\n", name+":", strings.Join(parts, ", "))
	keys := make([]string, 0, len(c.Reasons))
	for k := range c.Reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "               - %s: %d\n", k, c.Reasons[k])
	}
}

// Write prints the report as plain text.
func (r *Report) Write(w io.Writer) {
	mode := "IMPORT"
	if r.DryRun {
		mode = "DRY RUN (nothing was written)"
	}
	fmt.Fprintf(w, "arena-shop-import report: %s\n", mode)
	if r.OrgName != "" {
		fmt.Fprintf(w, "  organization: %s\n", r.OrgName)
	}
	if r.Offline {
		fmt.Fprintln(w, "  no database: every product, page and customer counts as new, nothing was compared")
	}
	writeCounter(w, "settings", &r.Settings, r.DryRun)
	writeCounter(w, "products", &r.Products, r.DryRun)
	writeCounter(w, "variants", &r.Variants, r.DryRun)
	writeCounter(w, "images", &r.Images, r.DryRun)
	writeCounter(w, "pages", &r.Pages, r.DryRun)
	writeCounter(w, "customers", &r.Customers, r.DryRun)
	writeCounter(w, "consents", &r.Consents, r.DryRun)
	writeCounter(w, "sms", &r.ConsentsSMS, r.DryRun)
	writeCounter(w, "whatsapp", &r.ConsentsWhatsApp, r.DryRun)
	if r.BodyImagesDropped > 0 || r.BodyTablesDropped > 0 {
		fmt.Fprintf(w, "  texts: the shop's sanitizer removed %d pictures and %d tables from descriptions and pages\n",
			r.BodyImagesDropped, r.BodyTablesDropped)
	}
	if r.StockValueMinor != 0 || r.MaxPriceMinor != 0 {
		fmt.Fprintf(w, "  money (%s): prices %s - %s, stock value %s\n", r.Currency,
			FormatMinor(r.MinPriceMinor, r.Currency), FormatMinor(r.MaxPriceMinor, r.Currency),
			FormatMinor(r.StockValueMinor, r.Currency))
	}
	if o := r.Orders; o != nil {
		fmt.Fprintf(w, "  orders file (parsed, NOT imported): %d orders, %d lines, %d without e-mail, %d cancelled, %d unreadable\n",
			o.Orders, o.Lines, o.WithoutEmail, o.Cancelled, o.TotalProblems)
		var states []string
		for _, k := range sortedKeys(o.ByState) {
			states = append(states, fmt.Sprintf("%s %d", k, o.ByState[k]))
		}
		fmt.Fprintf(w, "               by financial status: %s\n", strings.Join(states, ", "))
		for _, cur := range sortedKeys(o.ByCurrency) {
			t := o.ByCurrency[cur]
			fmt.Fprintf(w, "               %s: total %s, subtotal %s, shipping %s, discounts %s, refunded %s\n", cur,
				FormatMinor(t.Total, cur), FormatMinor(t.Subtotal, cur), FormatMinor(t.Shipping, cur),
				FormatMinor(t.Discount, cur), FormatMinor(t.Refunded, cur))
		}
	}
	if len(r.MissingImages) > 0 {
		fmt.Fprintf(w, "  missing images: %d\n", len(r.MissingImages))
		for _, m := range r.MissingImages {
			fmt.Fprintf(w, "    - %s\n", m)
		}
	}
	if len(r.Problems) > 0 {
		fmt.Fprintf(w, "  problems: %d\n", len(r.Problems))
		for _, p := range r.Problems {
			fmt.Fprintf(w, "    - %s\n", p)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "  note: %s\n", n)
	}
}
