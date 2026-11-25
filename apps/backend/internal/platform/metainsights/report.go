package metainsights

// report.go - the organizer's Meta ads reports (spec 66 section 5, stage M2):
// Meta's numbers from ad_spend_daily and the platform's own from orders and
// checkout_sessions. Pure SQL and Go integers: no float touches money.
//
// Linking (design contract M2): an order or a checkout is "from Meta ads"
// when its utm_campaign (orders.attribution first, then the checkout's) is a
// campaign_id of the organization's ad spend. A campaign belongs to an event
// when a checkout of that event ever carried its id; a campaign of more than
// one event is shared and its spend is not split. Complimentary, test,
// legacy-imported and Bil24-gateway orders and checkouts of test channels are
// never counted; the refunds are the succeeded ones of the sales
// funnel (gen/sales_funnel.sql.go listFunnelServerRows).

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

// Period limits.
const (
	// DefaultPeriodDays is the period when neither from nor to is given.
	DefaultPeriodDays = 30
	// MaxPeriodDays is the longest period a report reads, both days included.
	MaxPeriodDays = 400
	// VolumeDays is the window of the call volume (Access Tier: 15 days).
	VolumeDays = 15
)

// Period error codes.
const (
	ErrCodeInvalidDate  = "meta_ads.invalid_date"
	ErrCodeInvalidRange = "meta_ads.invalid_range"
	ErrCodeRangeTooLong = "meta_ads.range_too_long"
)

// PeriodError is a bad from/to: Code is the error envelope code.
type PeriodError struct {
	Code    string
	Message string
}

func (e *PeriodError) Error() string { return e.Message }

const dateLayout = "2006-01-02"

// ParsePeriod reads the from/to query values (YYYY-MM-DD, both optional).
// The default is the last DefaultPeriodDays days up to today in loc (nil =
// UTC); only from given means from..today, only to given means the 30 days
// ending at to. The days come back as UTC midnights.
func ParsePeriod(fromStr, toStr string, loc *time.Location, now time.Time) (from, to time.Time, perr *PeriodError) {
	if loc == nil {
		loc = time.UTC
	}
	l := now.In(loc)
	to = time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	fromStr, toStr = strings.TrimSpace(fromStr), strings.TrimSpace(toStr)
	if toStr != "" {
		t, err := time.Parse(dateLayout, toStr) // allow:timeformat: a calendar date
		if err != nil {
			return from, to, &PeriodError{ErrCodeInvalidDate, "to must be a date like 2026-05-31"}
		}
		to = t
	}
	from = to.AddDate(0, 0, -(DefaultPeriodDays - 1))
	if fromStr != "" {
		t, err := time.Parse(dateLayout, fromStr) // allow:timeformat: a calendar date
		if err != nil {
			return from, to, &PeriodError{ErrCodeInvalidDate, "from must be a date like 2026-05-01"}
		}
		from = t
	}
	if from.After(to) {
		return from, to, &PeriodError{ErrCodeInvalidRange, "from must not be after to"}
	}
	if int(to.Sub(from).Hours()/24)+1 > MaxPeriodDays {
		return from, to, &PeriodError{ErrCodeRangeTooLong, "ask for at most 400 days at a time"}
	}
	return from, to, nil
}

// Sync statuses of SyncInfo.Status.
const (
	SyncNotConnected = "not_connected"
	SyncNoAdAccount  = "no_ad_account"
	SyncOK           = "ok"
	SyncError        = "error"
)

// SyncInfo is the state of the daily read for the answers.
type SyncInfo struct {
	Status        string
	AdAccountID   string // the selected ad account, "" when none
	AdAccountName string
	Currency      string
	Timezone      string
	LastSuccessAt *time.Time
	LastError     string
	BackfillDone  bool
	// HistoryAccount is the account whose rows the reports read: the
	// selected one, else the one of the stored history; "" = no row at all
	// (never every account: their currencies may differ).
	HistoryAccount string
}

// Location is the ad account's timezone, UTC when unknown.
func (s SyncInfo) Location() *time.Location { return accountLocation(s.Timezone) }

// LoadSyncInfo reads the connection, the selected ad account and the sync
// state of orgID.
func LoadSyncInfo(ctx context.Context, q Querier, orgID uuid.UUID) (SyncInfo, error) {
	var info SyncInfo
	var status string
	var acct, name, cur, tz *string
	err := q.QueryRow(ctx, `
		SELECT c.status, a.meta_id, a.name, a.currency, a.timezone
		  FROM org_meta_connections c
		  LEFT JOIN org_meta_assets a ON a.org_id = c.org_id AND a.kind = 'ad_account' AND a.selected
		 WHERE c.org_id = $1
		 LIMIT 1`, orgID).Scan(&status, &acct, &name, &cur, &tz)
	connected := true
	if errors.Is(err, pgx.ErrNoRows) {
		connected = false
	} else if err != nil {
		return SyncInfo{}, err
	}
	st, err := LoadState(ctx, q, orgID)
	if err != nil {
		return SyncInfo{}, err
	}
	info.AdAccountID = deref(acct)
	info.AdAccountName = deref(name)
	info.Currency = strings.ToUpper(deref(cur))
	info.Timezone = deref(tz)
	info.LastSuccessAt = st.LastSuccessAt
	info.LastError = st.LastError
	info.BackfillDone = st.BackfillDoneAt != nil && (info.AdAccountID == "" || st.AdAccountID == info.AdAccountID)
	switch {
	case !connected || status != metaconnect.StatusActive:
		info.Status = SyncNotConnected
	case info.AdAccountID == "":
		info.Status = SyncNoAdAccount
	case st.LastError != "":
		info.Status = SyncError
	default:
		info.Status = SyncOK
	}
	info.HistoryAccount = info.AdAccountID
	if info.HistoryAccount == "" {
		info.HistoryAccount = st.AdAccountID
	}
	return info, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// EventName reads the name of an event of orgID; ok is false when the event
// is not in that organization or is deleted.
func EventName(ctx context.Context, q Querier, orgID, eventID uuid.UUID) (string, bool, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT name FROM events WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, eventID, orgID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// MetaTotals are Meta's numbers summed.
type MetaTotals struct {
	SpendMinor         int64
	Currency           string
	Impressions        int64
	Clicks             int64
	LinkClicks         int64
	Purchases          int64
	PurchaseValueMinor int64
}

// add sums o into m. Totals of another currency are never added: add
// reports false and m is unchanged.
func (m *MetaTotals) add(o MetaTotals) bool {
	if m.Currency != "" && o.Currency != "" && !strings.EqualFold(m.Currency, o.Currency) {
		return false
	}
	m.SpendMinor += o.SpendMinor
	m.Impressions += o.Impressions
	m.Clicks += o.Clicks
	m.LinkClicks += o.LinkClicks
	m.Purchases += o.Purchases
	m.PurchaseValueMinor += o.PurchaseValueMinor
	if m.Currency == "" {
		m.Currency = o.Currency
	}
	return true
}

// EventRef is an event a campaign is linked to.
type EventRef struct {
	EventID uuid.UUID
	Name    string
}

// Money is our paid orders and net revenue in one currency ("" = none).
type Money struct {
	Currency   string
	PaidOrders int64
	NetMinor   int64
}

// CampaignReport is one campaign with Meta's numbers and ours.
type CampaignReport struct {
	CampaignID      string
	Name            string
	EffectiveStatus string
	Meta            MetaTotals
	// InPeriod is true when the campaign has a spend row in the period.
	InPeriod bool
	Ours     Money
	Shared   bool
	Events   []EventRef
}

// OurTotals are the platform's own steps of the Meta-labelled orders.
type OurTotals struct {
	CheckoutsStarted int64
	Money
}

// Untagged is the share of paid orders without utm_campaign.
type Untagged struct {
	PaidOrders     int64
	UntaggedOrders int64
}

// EventReport is the Meta ads report of one event.
type EventReport struct {
	Meta        MetaTotals
	Ours        OurTotals
	MER         string // "" = null
	Untagged    Untagged
	Campaigns   []CampaignReport
	SharedSpend bool
}

// EventRow is one event line of the organization summary.
type EventRow struct {
	EventID    uuid.UUID
	Name       string
	SpendMinor int64
	Currency   string
	Ours       Money
	MER        string
}

// OrgSummary is the Meta ads summary of an organization.
type OrgSummary struct {
	Meta       MetaTotals
	Campaigns  []CampaignReport
	Events     []EventRow
	CallsTotal int64
	CallErrors int64
}

// window is the period and scope of one report read.
type window struct {
	org     uuid.UUID
	account string
	from    time.Time
	to      time.Time
	zone    string
	event   *uuid.UUID
}

func (w window) fromS() string { return w.from.Format(dateLayout) }
func (w window) toS() string   { return w.to.Format(dateLayout) }

// gatewayChannelOf is the body of a NOT EXISTS that leaves out an order of
// a Bil24-gateway channel (orders alias o), exactly like the sales funnel
// (gen/sales_funnel.sql.go listFunnelServerRows).
const gatewayChannelOf = `
	SELECT 1 FROM sales_channels gsc
	 WHERE gsc.id = o.channel_id
	   AND (gsc.settings #>> '{gateway,token_hash}' IS NOT NULL
	        OR gsc.settings ->> 'gateway_token_hash' IS NOT NULL)`

// campaignsCTE is the set of the organization's campaigns (all time) of ONE
// ad account with the last known name and currency. Params: $1 org, $2
// account; an empty account matches no row (never every account, whose
// currencies may differ).
const campaignsCTE = `camps AS (
	SELECT campaign_id,
	       (array_agg(campaign_name ORDER BY day DESC))[1] AS name,
	       (array_agg(currency ORDER BY day DESC))[1] AS currency
	  FROM ad_spend_daily
	 WHERE org_id = $1 AND ad_account_id = $2::text
	 GROUP BY campaign_id)`

// loadCampaigns reads every campaign of the organization with its spend in
// the period, biggest spend first.
func loadCampaigns(ctx context.Context, q Querier, w window) ([]CampaignReport, error) {
	rows, err := q.Query(ctx, `WITH `+campaignsCTE+`,
	p AS (
		SELECT campaign_id, max(currency) AS currency,
		       sum(spend_minor)::bigint AS spend, sum(impressions)::bigint AS impressions,
		       sum(clicks)::bigint AS clicks, sum(link_clicks)::bigint AS link_clicks,
		       sum(meta_purchases)::bigint AS purchases, sum(meta_purchase_value_minor)::bigint AS purchase_value
		  FROM ad_spend_daily
		 WHERE org_id = $1 AND ad_account_id = $2::text
		   AND day BETWEEN $3::date AND $4::date
		 GROUP BY campaign_id)
	SELECT c.campaign_id, COALESCE(NULLIF(ac.name, ''), c.name, ''), COALESCE(ac.effective_status, ''),
	       COALESCE(p.currency, c.currency, ''),
	       COALESCE(p.spend, 0), COALESCE(p.impressions, 0), COALESCE(p.clicks, 0), COALESCE(p.link_clicks, 0),
	       COALESCE(p.purchases, 0), COALESCE(p.purchase_value, 0), p.campaign_id IS NOT NULL
	  FROM camps c
	  LEFT JOIN p ON p.campaign_id = c.campaign_id
	  LEFT JOIN ad_campaigns ac ON ac.org_id = $1 AND ac.campaign_id = c.campaign_id
	 ORDER BY COALESCE(p.spend, 0) DESC, c.campaign_id`, w.org, w.account, w.fromS(), w.toS())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CampaignReport
	for rows.Next() {
		var c CampaignReport
		if err := rows.Scan(&c.CampaignID, &c.Name, &c.EffectiveStatus, &c.Meta.Currency,
			&c.Meta.SpendMinor, &c.Meta.Impressions, &c.Meta.Clicks, &c.Meta.LinkClicks,
			&c.Meta.Purchases, &c.Meta.PurchaseValueMinor, &c.InPeriod); err != nil {
			return nil, err
		}
		c.Meta.Currency = strings.ToUpper(c.Meta.Currency)
		out = append(out, c)
	}
	return out, rows.Err()
}

// loadLinks reads campaign -> events: a checkout of the event ever carried
// the campaign id in utm_campaign. Deleted events are left out.
func loadLinks(ctx context.Context, q Querier, w window) (map[string][]EventRef, error) {
	rows, err := q.Query(ctx, `WITH `+campaignsCTE+`
	SELECT l.camp, e.id, e.name
	  FROM (
	        SELECT DISTINCT cs.attribution ->> 'utm_campaign' AS camp, s.event_id
	          FROM checkout_sessions cs
	          JOIN reservations r ON r.id = cs.reservation_id
	          JOIN sessions s ON s.id = r.session_id
	          JOIN sales_channels sc ON sc.id = cs.channel_id
	         WHERE cs.org_id = $1
	           AND NOT sc.is_test
	           AND cs.attribution ->> 'utm_campaign' IN (SELECT campaign_id FROM camps)
	       ) l
	  JOIN events e ON e.id = l.event_id AND e.org_id = $1 AND e.deleted_at IS NULL
	 ORDER BY l.camp, e.name, e.id`, w.org, w.account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]EventRef{}
	for rows.Next() {
		var camp string
		var ref EventRef
		if err := rows.Scan(&camp, &ref.EventID, &ref.Name); err != nil {
			return nil, err
		}
		out[camp] = append(out[camp], ref)
	}
	return out, rows.Err()
}

// orderAgg is the paid orders of one (event, Meta campaign or "", tagged,
// currency).
type orderAgg struct {
	event    uuid.UUID
	campaign string // a Meta campaign id, "" when the order has none of them
	tagged   bool   // the order carries any utm_campaign
	currency string
	orders   int64
	net      int64
}

// loadOrders reads the paid orders of the period (order created_at in the
// account timezone), net of succeeded refunds, complimentary and test
// orders left out.
func loadOrders(ctx context.Context, q Querier, w window) ([]orderAgg, error) {
	rows, err := q.Query(ctx, `WITH `+campaignsCTE+`,
	x AS (
		SELECT o.event_id,
		       COALESCE(NULLIF(o.attribution ->> 'utm_campaign', ''), NULLIF(cs.attribution ->> 'utm_campaign', ''), '') AS camp,
		       o.currency::text AS currency,
		       o.total - rf.amount AS net
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
		   AND o.source NOT IN ('complimentary', 'legacy_import')
		   AND NOT o.is_test
		   AND o.status IN ('paid', 'partially_refunded', 'refunded')
		   AND NOT EXISTS (`+gatewayChannelOf+`)
		   AND o.created_at >= $3::date - 1 AND o.created_at < $4::date + 2
		   AND (o.created_at AT TIME ZONE $5::text)::date BETWEEN $3::date AND $4::date
		   AND ($6::uuid IS NULL OR o.event_id = $6::uuid))
	SELECT x.event_id,
	       CASE WHEN x.camp IN (SELECT campaign_id FROM camps) THEN x.camp ELSE '' END,
	       x.camp <> '',
	       x.currency, count(*)::bigint, COALESCE(sum(x.net), 0)::bigint
	  FROM x
	 GROUP BY 1, 2, 3, 4`, w.org, w.account, w.fromS(), w.toS(), w.zone, w.event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []orderAgg
	for rows.Next() {
		var a orderAgg
		if err := rows.Scan(&a.event, &a.campaign, &a.tagged, &a.currency, &a.orders, &a.net); err != nil {
			return nil, err
		}
		a.currency = strings.ToUpper(strings.TrimSpace(a.currency))
		out = append(out, a)
	}
	return out, rows.Err()
}

// loadCheckouts counts the checkouts of the period whose utm_campaign is a
// Meta campaign, per event; test channels are left out.
func loadCheckouts(ctx context.Context, q Querier, w window) (map[uuid.UUID]int64, error) {
	rows, err := q.Query(ctx, `WITH `+campaignsCTE+`
	SELECT s.event_id, count(*)::bigint
	  FROM checkout_sessions cs
	  JOIN reservations r ON r.id = cs.reservation_id
	  JOIN sessions s ON s.id = r.session_id
	  JOIN sales_channels sc ON sc.id = cs.channel_id
	 WHERE cs.org_id = $1
	   AND NOT sc.is_test
	   AND cs.attribution ->> 'utm_campaign' IN (SELECT campaign_id FROM camps)
	   AND cs.created_at >= $3::date - 1 AND cs.created_at < $4::date + 2
	   AND (cs.created_at AT TIME ZONE $5::text)::date BETWEEN $3::date AND $4::date
	   AND ($6::uuid IS NULL OR s.event_id = $6::uuid)
	 GROUP BY 1`, w.org, w.account, w.fromS(), w.toS(), w.zone, w.event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int64{}
	for rows.Next() {
		var ev uuid.UUID
		var n int64
		if err := rows.Scan(&ev, &n); err != nil {
			return nil, err
		}
		out[ev] = n
	}
	return out, rows.Err()
}

// pickMoney folds per-currency sums into one Money: the only currency, else
// the one equal to prefer, else the one with most orders (then by code).
// Sums of different currencies are never added together.
func pickMoney(by map[string]*Money, prefer string) Money {
	if len(by) == 0 {
		return Money{}
	}
	if m, ok := by[strings.ToUpper(prefer)]; ok && prefer != "" {
		return *m
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := by[keys[i]], by[keys[j]]
		if a.PaidOrders != b.PaidOrders {
			return a.PaidOrders > b.PaidOrders
		}
		return keys[i] < keys[j]
	})
	return *by[keys[0]]
}

func addMoney(by map[string]*Money, cur string, orders, net int64) {
	m := by[cur]
	if m == nil {
		m = &Money{Currency: cur}
		by[cur] = m
	}
	m.PaidOrders += orders
	m.NetMinor += net
}

// BuildEventReport reads the Meta ads report of eventID (which the caller
// checked belongs to orgID) for the period [from, to] in info's timezone.
func BuildEventReport(ctx context.Context, q Querier, orgID, eventID uuid.UUID, info SyncInfo, from, to time.Time) (EventReport, error) {
	w := window{org: orgID, account: info.HistoryAccount, from: from, to: to, zone: info.Location().String(), event: &eventID}
	camps, err := loadCampaigns(ctx, q, w)
	if err != nil {
		return EventReport{}, err
	}
	links, err := loadLinks(ctx, q, w)
	if err != nil {
		return EventReport{}, err
	}
	orders, err := loadOrders(ctx, q, w)
	if err != nil {
		return EventReport{}, err
	}
	checkouts, err := loadCheckouts(ctx, q, w)
	if err != nil {
		return EventReport{}, err
	}

	var rep EventReport
	rep.Meta.Currency = info.Currency
	perCamp := map[string]map[string]*Money{}
	ours := map[string]*Money{}
	for _, a := range orders {
		if a.event != eventID {
			continue
		}
		rep.Untagged.PaidOrders += a.orders
		if !a.tagged {
			rep.Untagged.UntaggedOrders += a.orders
		}
		if a.campaign == "" {
			continue
		}
		addMoney(ours, a.currency, a.orders, a.net)
		if perCamp[a.campaign] == nil {
			perCamp[a.campaign] = map[string]*Money{}
		}
		addMoney(perCamp[a.campaign], a.currency, a.orders, a.net)
	}
	for _, c := range camps {
		evs := links[c.CampaignID]
		if !linkedTo(evs, eventID) {
			continue
		}
		c.Events = evs
		c.Shared = len(evs) > 1
		c.Ours = pickMoney(perCamp[c.CampaignID], "")
		rep.Meta.add(c.Meta)
		if c.Shared {
			rep.SharedSpend = true
		}
		rep.Campaigns = append(rep.Campaigns, c)
	}
	rep.Ours.CheckoutsStarted = checkouts[eventID]
	rep.Ours.Money = pickMoney(ours, rep.Meta.Currency)
	rep.MER = MER(rep.Ours.NetMinor, rep.Ours.Currency, rep.Meta.SpendMinor, rep.Meta.Currency)
	return rep, nil
}

func linkedTo(evs []EventRef, id uuid.UUID) bool {
	for _, e := range evs {
		if e.EventID == id {
			return true
		}
	}
	return false
}

// BuildOrgSummary reads the Meta ads summary of orgID for the period: every
// campaign with spend in it, every event linked to a campaign, and the call
// volume of the last VolumeDays days up to now.
func BuildOrgSummary(ctx context.Context, q Querier, orgID uuid.UUID, info SyncInfo, from, to, now time.Time) (OrgSummary, error) {
	w := window{org: orgID, account: info.HistoryAccount, from: from, to: to, zone: info.Location().String()}
	camps, err := loadCampaigns(ctx, q, w)
	if err != nil {
		return OrgSummary{}, err
	}
	links, err := loadLinks(ctx, q, w)
	if err != nil {
		return OrgSummary{}, err
	}
	orders, err := loadOrders(ctx, q, w)
	if err != nil {
		return OrgSummary{}, err
	}

	var sum OrgSummary
	sum.Meta.Currency = info.Currency
	perCamp := map[string]map[string]*Money{}
	perEvent := map[uuid.UUID]map[string]*Money{}
	for _, a := range orders {
		if a.campaign == "" {
			continue
		}
		if perCamp[a.campaign] == nil {
			perCamp[a.campaign] = map[string]*Money{}
		}
		addMoney(perCamp[a.campaign], a.currency, a.orders, a.net)
		if perEvent[a.event] == nil {
			perEvent[a.event] = map[string]*Money{}
		}
		addMoney(perEvent[a.event], a.currency, a.orders, a.net)
	}

	events := map[uuid.UUID]*EventRow{}
	var order []uuid.UUID
	for _, c := range camps {
		evs := links[c.CampaignID]
		c.Events = evs
		c.Shared = len(evs) > 1
		c.Ours = pickMoney(perCamp[c.CampaignID], "")
		for _, e := range evs {
			row := events[e.EventID]
			if row == nil {
				row = &EventRow{EventID: e.EventID, Name: e.Name}
				events[e.EventID] = row
				order = append(order, e.EventID)
			}
			if row.Currency != "" && c.Meta.Currency != "" && !strings.EqualFold(row.Currency, c.Meta.Currency) {
				continue // never add spend of another currency
			}
			row.SpendMinor += c.Meta.SpendMinor
			if row.Currency == "" {
				row.Currency = c.Meta.Currency
			}
		}
		if !c.InPeriod {
			continue
		}
		sum.Meta.add(c.Meta)
		sum.Campaigns = append(sum.Campaigns, c)
	}
	for _, id := range order {
		row := *events[id]
		if row.Currency == "" {
			row.Currency = sum.Meta.Currency
		}
		row.Ours = pickMoney(perEvent[id], row.Currency)
		row.MER = MER(row.Ours.NetMinor, row.Ours.Currency, row.SpendMinor, row.Currency)
		sum.Events = append(sum.Events, row)
	}
	sort.SliceStable(sum.Events, func(i, j int) bool {
		a, b := sum.Events[i], sum.Events[j]
		if a.SpendMinor != b.SpendMinor {
			return a.SpendMinor > b.SpendMinor
		}
		return a.Name < b.Name
	})
	sum.CallsTotal, sum.CallErrors, err = CallVolume(ctx, q, orgID, now.UTC().AddDate(0, 0, -(VolumeDays-1)))
	if err != nil {
		return OrgSummary{}, err
	}
	return sum, nil
}
