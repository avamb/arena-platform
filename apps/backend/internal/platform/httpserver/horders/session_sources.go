package horders

import (
	"sort"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Source kinds of the session summary's "sources" block (spec 43 section 4.5).
const (
	sourceKindSalesLink = "sales_link"
	sourceKindUTM       = "utm"
	sourceKindDirect    = "direct"
)

// sourceRevenue is a source's revenue in one currency: paid orders' totals
// minus their succeeded refunds, in minor units.
type sourceRevenue struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// sessionSource is one row of the session summary's "sources" block: the paid
// orders of the session that came through one sales link, one utm pair, or
// neither (direct). It carries no buyer data.
type sessionSource struct {
	Kind        string          `json:"kind"`
	SalesLinkID *string         `json:"sales_link_id,omitempty"`
	Label       *string         `json:"label,omitempty"`
	UTMSource   *string         `json:"utm_source,omitempty"`
	UTMCampaign *string         `json:"utm_campaign,omitempty"`
	Orders      int64           `json:"orders"`
	Tickets     int64           `json:"tickets"`
	Revenue     []sourceRevenue `json:"revenue"`
}

func sourceKindRank(kind string) int {
	switch kind {
	case sourceKindSalesLink:
		return 0
	case sourceKindUTM:
		return 1
	default:
		return 2
	}
}

func strPtr(s string) *string { return &s }

// foldSources turns the per-(source, currency) rows of
// ListSessionSummarySources into one entry per source. Pure, so the grouping
// is unit-tested without a database. Never nil: a session without paid orders
// answers an empty list.
func foldSources(rows []gen.SessionSummarySourceRow) []sessionSource {
	type key struct{ link, utmSource, utmCampaign string }
	groups := map[key]*sessionSource{}
	order := []key{}
	for _, r := range rows {
		k := key{utmSource: r.UTMSource, utmCampaign: r.UTMCampaign}
		if r.SalesLinkID != nil {
			k = key{link: r.SalesLinkID.String()}
		}
		g := groups[k]
		if g == nil {
			g = &sessionSource{Revenue: []sourceRevenue{}}
			switch {
			case r.SalesLinkID != nil:
				g.Kind = sourceKindSalesLink
				g.SalesLinkID = strPtr(k.link)
				g.Label = strPtr(r.Label)
			case r.UTMSource != "":
				g.Kind = sourceKindUTM
				g.UTMSource = strPtr(r.UTMSource)
				if r.UTMCampaign != "" {
					g.UTMCampaign = strPtr(r.UTMCampaign)
				}
			default:
				g.Kind = sourceKindDirect
			}
			groups[k] = g
			order = append(order, k)
		}
		g.Orders += r.Orders
		g.Tickets += r.Tickets
		merged := false
		for i := range g.Revenue {
			if g.Revenue[i].Currency == r.Currency {
				g.Revenue[i].AmountMinor += r.Amount
				merged = true
				break
			}
		}
		if !merged {
			g.Revenue = append(g.Revenue, sourceRevenue{Currency: r.Currency, AmountMinor: r.Amount})
		}
	}
	out := make([]sessionSource, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.Slice(g.Revenue, func(i, j int) bool { return g.Revenue[i].Currency < g.Revenue[j].Currency })
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := sourceKindRank(a.Kind), sourceKindRank(b.Kind); ra != rb {
			return ra < rb
		}
		if a.Orders != b.Orders {
			return a.Orders > b.Orders
		}
		return sourceSortKey(a) < sourceSortKey(b)
	})
	return out
}

func sourceSortKey(s sessionSource) string {
	switch {
	case s.SalesLinkID != nil:
		return *s.Label + "\x00" + *s.SalesLinkID
	case s.UTMSource != nil:
		c := ""
		if s.UTMCampaign != nil {
			c = *s.UTMCampaign
		}
		return *s.UTMSource + "\x00" + c
	default:
		return ""
	}
}
