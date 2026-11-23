package metainsights

import (
	"context"
	"fmt"
	"html"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

// Summary is the daily ad summary of one day.
type Summary struct {
	Day         string // YYYY-MM-DD, ad account timezone
	Currency    string // ad account currency
	SpendMinor  int64
	Impressions int64
	LinkClicks  int64
	Purchases   int64
	// Ours are the paid orders of the day whose utm_campaign is a campaign
	// of the organization's ad spend, per order currency.
	Ours []OurMoney
}

// OurMoney is the platform's own count of paid orders and net revenue
// (total minus succeeded refunds) in one currency.
type OurMoney struct {
	Currency      string
	PaidOrders    int64
	RevenueNetMin int64
}

// SummaryKey is the sales_notification_deliveries key of the summary of
// day: one message per organization and day.
func SummaryKey(orgID uuid.UUID, day string) string { return "ads:" + orgID.String() + ":" + day }

// MER is revenue / spend with two decimals, or "" when the currencies differ
// or the spend is zero.
func MER(revenueMinor int64, revenueCurrency string, spendMinor int64, spendCurrency string) string {
	if spendMinor <= 0 || !strings.EqualFold(revenueCurrency, spendCurrency) {
		return ""
	}
	r := new(big.Rat).SetFrac(big.NewInt(revenueMinor), big.NewInt(spendMinor))
	return r.FloatString(2)
}

// SummaryText is the English HTML text of the daily summary, like the other
// sales bot texts.
func SummaryText(s Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Meta ads, %s</b>\n", html.EscapeString(s.Day))
	fmt.Fprintf(&b, "Spend: %s\n", html.EscapeString(FormatMinor(s.SpendMinor, s.Currency)))
	fmt.Fprintf(&b, "Impressions: %d\n", s.Impressions)
	fmt.Fprintf(&b, "Link clicks: %d\n", s.LinkClicks)
	fmt.Fprintf(&b, "Purchases reported by Meta: %d\n", s.Purchases)
	if len(s.Ours) == 0 {
		b.WriteString("Our paid orders from these ads: 0")
	}
	for i, o := range s.Ours {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Our paid orders from these ads: %d, net %s", o.PaidOrders, html.EscapeString(FormatMinor(o.RevenueNetMin, o.Currency)))
		if m := MER(o.RevenueNetMin, o.Currency, s.SpendMinor, s.Currency); m != "" {
			fmt.Fprintf(&b, "\nMER (net revenue / spend): %s", m)
		}
	}
	return b.String()
}

// loadSummary reads the summary of day for the ad account. ok is false when
// the account has no row that day (no ads ran: nothing is sent).
func (s *Syncer) loadSummary(ctx context.Context, orgID uuid.UUID, account, currency, day, tz string) (Summary, bool, error) {
	sum := Summary{Day: day, Currency: strings.ToUpper(currency)}
	var n int64
	var cur *string
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(spend_minor), 0)::bigint, COALESCE(sum(impressions), 0)::bigint,
		       COALESCE(sum(link_clicks), 0)::bigint, COALESCE(sum(meta_purchases), 0)::bigint, max(currency)
		  FROM ad_spend_daily WHERE org_id = $1 AND ad_account_id = $2 AND day = $3::date`,
		orgID, account, day).Scan(&n, &sum.SpendMinor, &sum.Impressions, &sum.LinkClicks, &sum.Purchases, &cur); err != nil {
		return Summary{}, false, err
	}
	if n == 0 {
		return Summary{}, false, nil
	}
	if cur != nil && *cur != "" {
		sum.Currency = *cur
	}
	rows, err := s.pool.Query(ctx, `
		SELECT o.currency::text, count(*)::bigint, COALESCE(sum(o.total - rf.amount), 0)::bigint
		  FROM orders o
		  LEFT JOIN checkout_sessions cs ON cs.id = o.checkout_session_id
		  LEFT JOIN LATERAL (
		      SELECT COALESCE(sum(r.amount), 0)::bigint AS amount
		        FROM (
		              SELECT r1.id FROM refunds r1 WHERE r1.order_id = o.id
		              UNION
		              SELECT r2.id FROM refunds r2 JOIN tickets t ON t.id = r2.ticket_id WHERE t.order_id = o.id
		              UNION
		              SELECT r3.id FROM refunds r3 JOIN payment_intents p3 ON p3.id = r3.payment_intent_id
		               WHERE p3.checkout_session_id = o.checkout_session_id
		             ) cand
		        JOIN refunds r ON r.id = cand.id
		       WHERE r.state = 'succeeded'
		  ) rf ON true
		 WHERE o.org_id = $1
		   AND o.source <> 'complimentary'
		   AND NOT o.is_test
		   AND o.status IN ('paid', 'partially_refunded', 'refunded')
		   AND o.created_at >= $2::date - 1 AND o.created_at < $2::date + 2
		   AND (o.created_at AT TIME ZONE $3::text)::date = $2::date
		   AND COALESCE(NULLIF(o.attribution ->> 'utm_campaign', ''), cs.attribution ->> 'utm_campaign')
		       IN (SELECT DISTINCT campaign_id FROM ad_spend_daily WHERE org_id = $1)
		 GROUP BY o.currency
		 ORDER BY o.currency`, orgID, day, tz)
	if err != nil {
		return Summary{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var m OurMoney
		if err := rows.Scan(&m.Currency, &m.PaidOrders, &m.RevenueNetMin); err != nil {
			return Summary{}, false, err
		}
		sum.Ours = append(sum.Ours, m)
	}
	return sum, true, rows.Err()
}

// sendSummary posts the summary of yesterday (account timezone) to the
// organization's chats that hear salesnotify.KindAds, once per day.
func (s *Syncer) sendSummary(ctx context.Context, orgID uuid.UUID, account, currency string, loc *time.Location, now time.Time) (bool, error) {
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Format("2006-01-02") // allow:timeformat: a calendar date
	sum, ok, err := s.loadSummary(ctx, orgID, account, currency, day, loc.String())
	if err != nil || !ok {
		return false, err
	}
	_, delivered, err := s.announcer.AnnounceKindToOrg(ctx, orgID.String(), salesnotify.KindAds, SummaryKey(orgID, day), SummaryText(sum))
	return delivered > 0, err
}
