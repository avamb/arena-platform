package legacytickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// AuthorityType is the barcode authority of the original Bil24 barcodes.
const AuthorityType = "legacy_bil24"

// AuthorityLabel names the authority when this tool has to create it.
const AuthorityLabel = "Bil24 (tickets sold before the platform)"

// PaymentMethod is orders.payment_method of an imported order.
const PaymentMethod = "bil24"

// OrderEventType is the order_events row each imported order gets.
const OrderEventType = "legacy_imported"

// RefuseWriteFailed is an order the database refused while writing; nothing
// of it was kept.
const RefuseWriteFailed = "write_failed"

// DB is a pgx pool: the plan reads through it, every order is written in a
// transaction of its own.
type DB interface {
	gen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Options steer one run.
type Options struct {
	OrgID uuid.UUID
	// ChannelID is the sales channel the orders are filed under. uuid.Nil:
	// the channel the session's Bil24 gateway orders came through, else the
	// organization's only gateway channel.
	ChannelID uuid.UUID
	// OnlyFuture skips sessions that have already started.
	OnlyFuture bool
	// Apply writes; false is the dry run, which only reads.
	Apply bool
	Now   time.Time
}

// Places are one category's counters.
type Places struct {
	Capacity     int32 // ticket_tiers.capacity
	Places       int32 // GA places of the category
	Sold         int32
	SoldUpstream int32 // of Sold: no reservation behind the place
	Available    int32
}

// TierReport is one category of a session.
type TierReport struct {
	TierID  uuid.UUID
	Name    string
	Tickets int
	Before  Places
	After   Places // dry run: projected; apply: read back
}

// SessionReport is what a run found or wrote for one session.
type SessionReport struct {
	SessionID uuid.UUID
	EventName string
	StartAt   time.Time
	Currency  string
	ChannelID uuid.UUID
	Orders    int // orders to file (dry run) or filed (apply)
	Tickets   int
	SumMinor  int64
	Unchanged int // orders filed by an earlier run
	Tiers     []*TierReport
	SystemIDs []int64 // apply: the order numbers written
	// LedgerBefore / LedgerAfter are the session ledger's (total, sold).
	LedgerBefore [2]int32
	LedgerAfter  [2]int32
}

// Refusal is an order the run did not file and the operator has to look at.
type Refusal struct {
	WCOrderID    string
	Bil24OrderID int64
	Code         string
	Detail       string
}

// Report is the outcome of a run.
type Report struct {
	Apply     bool
	Orders    int // orders in the file
	Unchanged int
	Skipped   map[string]int
	SkipList  []Refusal
	Refusals  []Refusal
	Warnings  []string
	Sessions  []*SessionReport
	// AuthorityMissing: the legacy_bil24 barcode authority does not exist yet
	// (a dry run reports it, an apply creates it and sets AuthorityCreated).
	AuthorityMissing bool
	AuthorityCreated bool
}

type sessionInfo struct {
	ID        uuid.UUID
	EventID   uuid.UUID
	EventName string
	StartAt   time.Time
	Status    string
	Currency  string
	Country   string
	ChannelID uuid.UUID
	gaTiers   []tierInfo
	report    *SessionReport
	tiers     map[uuid.UUID]*TierReport
}

type tierInfo struct {
	ID   uuid.UUID
	Name string
}

type plannedTicket struct {
	PreparedTicket
	Money
	TierID uuid.UUID
}

type plannedOrder struct {
	p          Prepared
	sess       *sessionInfo
	tickets    []plannedTicket
	subtotal   int64
	discount   int64
	charge     int64
	total      int64
	externalOK bool // the WooCommerce id may go into orders.external_ref
}

type runner struct {
	db      DB
	opt     Options
	rep     *Report
	session map[uuid.UUID]*sessionInfo
	// what earlier orders of this file already claimed
	fileBarcodes map[string]string // barcode -> wc order
	fileTickets  map[int64]string
}

// Run files (Apply) or plans (dry run) the legacy orders of one organization.
// A refusal never stops the run: it is reported and the next order goes on.
// A returned error is a failure of the run itself (the database is gone).
func Run(ctx context.Context, db DB, opt Options, doc Export) (Report, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now().UTC()
	}
	rep := Report{Apply: opt.Apply, Orders: len(doc.Orders), Skipped: map[string]int{}}
	r := &runner{db: db, opt: opt, rep: &rep, session: map[uuid.UUID]*sessionInfo{},
		fileBarcodes: map[string]string{}, fileTickets: map[int64]string{}}

	if opt.ChannelID != uuid.Nil {
		var ok bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_channels WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL)`,
			opt.ChannelID, opt.OrgID).Scan(&ok); err != nil {
			return rep, fmt.Errorf("check channel: %w", err)
		}
		if !ok {
			return rep, fmt.Errorf("channel %s is not a live sales channel of the organization", opt.ChannelID)
		}
	}

	var planned []*plannedOrder
	for _, o := range doc.Orders {
		p, prob := Prepare(o)
		for _, w := range p.Warnings {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("wc_order=%s: %s", p.WCOrderID, w))
		}
		if prob == nil {
			var po *plannedOrder
			var err error
			po, prob, err = r.plan(ctx, p)
			if err != nil {
				return rep, err
			}
			if po != nil {
				planned = append(planned, po)
			}
		}
		if prob != nil {
			r.problem(p, prob)
		}
	}

	var authority uuid.UUID
	a, err := gen.New(db).GetBarcodeAuthorityByType(ctx, AuthorityType)
	switch {
	case err == nil:
		authority = a.ID
	case errors.Is(err, pgx.ErrNoRows):
		rep.AuthorityMissing = true
	default:
		return rep, fmt.Errorf("find the %s barcode authority: %w", AuthorityType, err)
	}

	if !opt.Apply {
		for _, po := range planned {
			r.count(po)
		}
		r.projectAfter()
		r.sortSessions()
		return rep, nil
	}

	if authority == uuid.Nil && len(planned) > 0 {
		a, err := gen.New(db).InsertBarcodeAuthority(ctx, AuthorityType, AuthorityLabel)
		if err != nil {
			return rep, fmt.Errorf("create the %s barcode authority: %w", AuthorityType, err)
		}
		authority = a.ID
		rep.AuthorityCreated = true
	}
	for _, po := range planned {
		sysID, unchanged, prob, err := r.apply(ctx, po, authority)
		switch {
		case err != nil:
			r.problem(po.p, &Problem{RefuseWriteFailed, err.Error()})
		case prob != nil:
			r.problem(po.p, prob)
		case unchanged:
			rep.Unchanged++
			po.sess.report.Unchanged++
		default:
			r.count(po)
			po.sess.report.SystemIDs = append(po.sess.report.SystemIDs, sysID)
		}
	}
	if err := r.readAfter(ctx); err != nil {
		return rep, err
	}
	r.sortSessions()
	return rep, nil
}

func (r *runner) problem(p Prepared, prob *Problem) {
	ref := Refusal{WCOrderID: p.WCOrderID, Bil24OrderID: p.Bil24OrderID, Code: prob.Code, Detail: prob.Detail}
	if prob.Skipped() {
		r.rep.Skipped[prob.Code]++
		r.rep.SkipList = append(r.rep.SkipList, ref)
		return
	}
	r.rep.Refusals = append(r.rep.Refusals, ref)
}

func (r *runner) count(po *plannedOrder) {
	sr := po.sess.report
	sr.Orders++
	sr.Tickets += len(po.tickets)
	sr.SumMinor += po.total
	for _, t := range po.tickets {
		po.sess.tiers[t.TierID].Tickets++
	}
}

// plan checks one order against the database without writing. It returns
// (nil, nil, nil) for an order an earlier run already filed (counted as
// unchanged here, in a dry run, and again under the lock in an apply).
func (r *runner) plan(ctx context.Context, p Prepared) (*plannedOrder, *Problem, error) {
	sessionID, prob, err := r.resolveSession(ctx, p)
	if err != nil || prob != nil {
		return nil, prob, err
	}
	sess, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if sess.Status == "cancelled" {
		return nil, &Problem{RefuseCancelledSession, "session " + sess.ID.String() + " is cancelled"}, nil
	}
	if r.opt.OnlyFuture && !sess.StartAt.After(r.opt.Now) {
		return nil, &Problem{SkipPastSession, fmt.Sprintf("session %s started %s", sess.ID, sess.StartAt.Format(time.RFC3339))}, nil
	}
	if sess.ChannelID == uuid.Nil {
		return nil, &Problem{RefuseNoChannel, "no gateway channel for the session and none is unique in the organization: pass --channel-id"}, nil
	}
	if p.Currency != "" && !strings.EqualFold(p.Currency, sess.Currency) {
		return nil, &Problem{RefuseCurrency, fmt.Sprintf("order currency %s, session currency %s", p.Currency, sess.Currency)}, nil
	}

	// Filed by an earlier run?
	var linkedOrder uuid.UUID
	err = r.db.QueryRow(ctx, `SELECT order_id FROM wp_order_links WHERE org_id = $1 AND wc_order_id = $2`,
		r.opt.OrgID, p.WCOrderID).Scan(&linkedOrder)
	switch {
	case err == nil:
		if prob, err := r.sameTickets(ctx, linkedOrder, p); err != nil || prob != nil {
			return nil, prob, err
		}
		r.rep.Unchanged++
		sess.report.Unchanged++
		return nil, nil, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, nil, fmt.Errorf("look up wp_order_links: %w", err)
	}

	po := &plannedOrder{p: p, sess: sess}
	scale := money.ScaleFor(sess.Currency)
	for _, t := range p.Tickets {
		if prior, dup := r.fileBarcodes[t.Barcode]; dup {
			return nil, &Problem{RefuseDuplicateInFile, fmt.Sprintf("barcode %s is also on WooCommerce order %s", t.Barcode, prior)}, nil
		}
		if prior, dup := r.fileTickets[t.Bil24TicketID]; dup {
			return nil, &Problem{RefuseDuplicateInFile, fmt.Sprintf("Bil24 ticket %d is also on WooCommerce order %s", t.Bil24TicketID, prior)}, nil
		}
		m, err := TicketMoney(t, scale)
		if err != nil {
			return nil, &Problem{RefuseMoney, err.Error()}, nil
		}
		tier, prob := pickTier(sess.gaTiers, t.Category)
		if prob != nil {
			return nil, prob, nil
		}
		if prob, err := r.barcodeFree(ctx, t); err != nil || prob != nil {
			return nil, prob, err
		}
		po.tickets = append(po.tickets, plannedTicket{PreparedTicket: t, Money: m, TierID: tier})
		po.subtotal += m.Price
		po.discount += m.Discount
		po.charge += m.Charge
	}
	po.total = po.subtotal - po.discount + po.charge

	ids := make([]int64, 0, len(p.Tickets))
	for _, t := range p.Tickets {
		ids = append(ids, t.Bil24TicketID)
	}
	var already int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM legacy_ticket_imports WHERE org_id = $1 AND bil24_ticket_id = ANY($2)`,
		r.opt.OrgID, ids).Scan(&already); err != nil {
		return nil, nil, fmt.Errorf("look up imported tickets: %w", err)
	}
	if already > 0 {
		return nil, &Problem{RefuseTicketImported, fmt.Sprintf("%d of the order's tickets were imported with another WooCommerce order", already)}, nil
	}
	var taken bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE system_id = $1)`, p.Bil24OrderID).Scan(&taken); err != nil {
		return nil, nil, fmt.Errorf("check order number: %w", err)
	}
	if taken {
		return nil, &Problem{RefuseOrderNumberTaken, fmt.Sprintf("a platform order already has number %d", p.Bil24OrderID)}, nil
	}
	// The ticket keeps its Bil24 number (MACS knows it by that number and
	// reports scans with it), so the number must still be free.
	var numbersTaken int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE system_ticket_id = ANY($1)`, ids).Scan(&numbersTaken); err != nil {
		return nil, nil, fmt.Errorf("check ticket numbers: %w", err)
	}
	if numbersTaken > 0 {
		return nil, &Problem{RefuseTicketNumberTaken, fmt.Sprintf("%d of the order's Bil24 ticket numbers already belong to platform tickets", numbersTaken)}, nil
	}
	var refTaken bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE channel_id = $1 AND external_ref = $2)`,
		sess.ChannelID, p.WCOrderID).Scan(&refTaken); err != nil {
		return nil, nil, fmt.Errorf("check external ref: %w", err)
	}
	po.externalOK = !refTaken
	if refTaken {
		r.rep.Warnings = append(r.rep.Warnings, fmt.Sprintf("wc_order=%s: another order of the channel carries this WooCommerce number, the import leaves orders.external_ref empty", p.WCOrderID))
	}
	for _, t := range p.Tickets {
		r.fileBarcodes[t.Barcode] = p.WCOrderID
		r.fileTickets[t.Bil24TicketID] = p.WCOrderID
	}
	return po, nil, nil
}

// sameTickets checks that an order filed earlier holds exactly the live
// tickets of the file.
func (r *runner) sameTickets(ctx context.Context, orderID uuid.UUID, p Prepared) (*Problem, error) {
	rows, err := r.db.Query(ctx, `SELECT bil24_ticket_id FROM legacy_ticket_imports WHERE order_id = $1 ORDER BY 1`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list imported tickets: %w", err)
	}
	defer rows.Close()
	var have []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		have = append(have, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	want := make([]int64, 0, len(p.Tickets))
	for _, t := range p.Tickets {
		want = append(want, t.Bil24TicketID)
	}
	if fmt.Sprint(have) != fmt.Sprint(want) {
		return &Problem{RefuseImportedDiffers, fmt.Sprintf("filed earlier with Bil24 tickets %v, the file now lists %v as live: change the platform order by hand", have, want)}, nil
	}
	return nil, nil
}

// barcodeFree refuses a barcode that any authority, or any ticket's EAN-13
// credential, already knows.
func (r *runner) barcodeFree(ctx context.Context, t PreparedTicket) (*Problem, error) {
	var where string
	err := r.db.QueryRow(ctx, `
SELECT 'barcode authority ' || a.type
  FROM barcodes b JOIN barcode_authorities a ON a.id = b.authority_id
 WHERE b.external_ref = $1
UNION ALL
SELECT 'ticket credential of ticket ' || c.ticket_id::text
  FROM ticket_credentials c
 WHERE c.type = 'ean13' AND c.payload = $1
LIMIT 1`, t.Barcode).Scan(&where)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check barcode: %w", err)
	}
	return &Problem{RefuseBarcodeTaken, fmt.Sprintf("Bil24 ticket %d: barcode %s is already known (%s)", t.Bil24TicketID, t.Barcode, where)}, nil
}

// pickTier is the session's only General Admission category, else the one
// named like the Bil24 ticket's category.
func pickTier(tiers []tierInfo, category string) (uuid.UUID, *Problem) {
	switch len(tiers) {
	case 0:
		return uuid.Nil, &Problem{RefuseNoGACategory, "the session has no General Admission category"}
	case 1:
		return tiers[0].ID, nil
	}
	want := strings.ToLower(strings.Join(strings.Fields(category), " "))
	var hit []uuid.UUID
	for _, t := range tiers {
		if strings.ToLower(strings.Join(strings.Fields(t.Name), " ")) == want && want != "" {
			hit = append(hit, t.ID)
		}
	}
	if len(hit) == 1 {
		return hit[0], nil
	}
	names := make([]string, 0, len(tiers))
	for _, t := range tiers {
		names = append(names, t.Name)
	}
	return uuid.Nil, &Problem{RefuseAmbiguousCategory, fmt.Sprintf("Bil24 category %q does not name one of the session's categories %q", category, names)}
}

// resolveSession finds the one platform session of all the order's tickets:
// through the Bil24 session id the import registered (compatibility_id_map),
// else the only live session of the event whose external_bil24_id is the
// Bil24 action.
func (r *runner) resolveSession(ctx context.Context, p Prepared) (uuid.UUID, *Problem, error) {
	found := uuid.Nil
	for _, t := range p.Tickets {
		id, prob, err := r.ticketSession(ctx, t)
		if err != nil || prob != nil {
			return uuid.Nil, prob, err
		}
		if found != uuid.Nil && id != found {
			return uuid.Nil, &Problem{RefuseMultipleSessions, "the order's tickets belong to more than one session: file it by hand"}, nil
		}
		found = id
	}
	return found, nil, nil
}

func (r *runner) ticketSession(ctx context.Context, t PreparedTicket) (uuid.UUID, *Problem, error) {
	if t.ActionEventID > 0 {
		var id uuid.UUID
		err := r.db.QueryRow(ctx, `
SELECT s.id
  FROM compatibility_id_map m
  JOIN sessions s ON s.id = m.platform_id
  JOIN events   e ON e.id = s.event_id
 WHERE m.kind = 'action_event' AND m.system_id = $1
   AND e.org_id = $2 AND s.deleted_at IS NULL AND e.deleted_at IS NULL`, t.ActionEventID, r.opt.OrgID).Scan(&id)
		if err == nil {
			return id, nil, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, nil, fmt.Errorf("look up Bil24 session %d: %w", t.ActionEventID, err)
		}
	}
	if t.ActionID <= 0 {
		return uuid.Nil, &Problem{RefuseUnknownAction, fmt.Sprintf("Bil24 ticket %d: no Bil24 action on the order's items and Bil24 session %d is not known", t.Bil24TicketID, t.ActionEventID)}, nil
	}
	rows, err := r.db.Query(ctx, `
SELECT s.id, s.status, s.start_at
  FROM events e
  JOIN sessions s ON s.event_id = e.id AND s.deleted_at IS NULL
 WHERE e.org_id = $1 AND e.external_bil24_id = $2 AND e.deleted_at IS NULL
 ORDER BY s.start_at`, r.opt.OrgID, strconv.FormatInt(t.ActionID, 10))
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("look up Bil24 action %d: %w", t.ActionID, err)
	}
	defer rows.Close()
	var live []uuid.UUID
	var starts []string
	total := 0
	for rows.Next() {
		var id uuid.UUID
		var status string
		var start time.Time
		if err := rows.Scan(&id, &status, &start); err != nil {
			return uuid.Nil, nil, err
		}
		total++
		if status != "cancelled" {
			live = append(live, id)
			starts = append(starts, start.UTC().Format(time.RFC3339))
		}
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, nil, err
	}
	switch {
	case total == 0:
		var events int
		if err := r.db.QueryRow(ctx, `SELECT count(*) FROM events WHERE org_id = $1 AND external_bil24_id = $2 AND deleted_at IS NULL`,
			r.opt.OrgID, strconv.FormatInt(t.ActionID, 10)).Scan(&events); err != nil {
			return uuid.Nil, nil, err
		}
		if events == 0 {
			return uuid.Nil, &Problem{RefuseUnknownAction, fmt.Sprintf("no event of the organization has external_bil24_id %d", t.ActionID)}, nil
		}
		return uuid.Nil, &Problem{RefuseUnknownSession, fmt.Sprintf("the event of Bil24 action %d has no session", t.ActionID)}, nil
	case len(live) == 0:
		return uuid.Nil, &Problem{RefuseCancelledSession, fmt.Sprintf("every session of Bil24 action %d is cancelled", t.ActionID)}, nil
	case len(live) > 1:
		return uuid.Nil, &Problem{RefuseAmbiguousSession, fmt.Sprintf("Bil24 action %d has %d live sessions %v and Bil24 session %d is not registered", t.ActionID, len(live), starts, t.ActionEventID)}, nil
	}
	return live[0], nil, nil
}

func (r *runner) loadSession(ctx context.Context, id uuid.UUID) (*sessionInfo, error) {
	if s, ok := r.session[id]; ok {
		return s, nil
	}
	s := &sessionInfo{ID: id, tiers: map[uuid.UUID]*TierReport{}}
	if err := r.db.QueryRow(ctx, `
SELECT s.event_id, e.name, s.start_at, s.status, s.currency::text, COALESCE(v.country::text, '')
  FROM sessions s
  JOIN events e ON e.id = s.event_id
  LEFT JOIN venues v ON v.id = s.venue_id
 WHERE s.id = $1 AND e.org_id = $2`, id, r.opt.OrgID).
		Scan(&s.EventID, &s.EventName, &s.StartAt, &s.Status, &s.Currency, &s.Country); err != nil {
		return nil, fmt.Errorf("load session %s: %w", id, err)
	}
	s.Currency = strings.TrimSpace(s.Currency)
	rows, err := r.db.Query(ctx, `
SELECT t.id, t.name
  FROM ticket_tiers t
 WHERE t.session_id = $1 AND t.deleted_at IS NULL AND NOT t.is_addon
   AND NOT EXISTS (SELECT 1 FROM session_seats ss
                    WHERE ss.session_id = t.session_id AND ss.tier_id = t.id AND ss.kind = 'seat')
 ORDER BY t.sort_order, t.name, t.id`, id)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	for rows.Next() {
		var t tierInfo
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			rows.Close()
			return nil, err
		}
		s.gaTiers = append(s.gaTiers, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if s.ChannelID, err = r.channelFor(ctx, id); err != nil {
		return nil, err
	}
	s.report = &SessionReport{SessionID: id, EventName: s.EventName, StartAt: s.StartAt, Currency: s.Currency, ChannelID: s.ChannelID}
	q := gen.New(r.db)
	for _, t := range s.gaTiers {
		pl, err := readPlaces(ctx, r.db, q, id, t.ID)
		if err != nil {
			return nil, err
		}
		tr := &TierReport{TierID: t.ID, Name: t.Name, Before: pl}
		s.tiers[t.ID] = tr
		s.report.Tiers = append(s.report.Tiers, tr)
	}
	if s.report.LedgerBefore, err = readLedger(ctx, r.db, id); err != nil {
		return nil, err
	}
	r.session[id] = s
	return s, nil
}

// channelFor is the sales channel legacy orders of a session are filed
// under: the override, else the channel most of the session's Bil24 gateway
// orders came through, else the organization's only channel holding a
// gateway token. uuid.Nil when none of that decides.
func (r *runner) channelFor(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error) {
	if r.opt.ChannelID != uuid.Nil {
		return r.opt.ChannelID, nil
	}
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `
SELECT o.channel_id
  FROM orders o
  JOIN sales_channels c ON c.id = o.channel_id AND c.deleted_at IS NULL
 WHERE o.session_id = $1 AND o.org_id = $2 AND o.source = 'bil24_gateway'
 GROUP BY o.channel_id
 ORDER BY count(*) DESC, o.channel_id
 LIMIT 1`, sessionID, r.opt.OrgID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("find the session's gateway channel: %w", err)
	}
	rows, err := r.db.Query(ctx, `
SELECT id FROM sales_channels
 WHERE org_id = $1 AND deleted_at IS NULL
   AND (COALESCE(settings->'gateway'->>'token_hash', '') <> '' OR COALESCE(settings->>'gateway_token_hash', '') <> '')`, r.opt.OrgID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list gateway channels: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, err
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	return uuid.Nil, nil
}

func readPlaces(ctx context.Context, db gen.DBTX, q *gen.Queries, sessionID, tierID uuid.UUID) (Places, error) {
	st, err := gaquota.CategoryStats(ctx, q, sessionID, tierID)
	if err != nil {
		return Places{}, err
	}
	pl := Places{Places: st.Quantity, Sold: st.Sold, Available: st.Available}
	var capacity *int32
	if err := db.QueryRow(ctx, `
SELECT t.capacity,
       (SELECT count(*)::int FROM session_seats ss
         WHERE ss.session_id = t.session_id AND ss.tier_id = t.id AND ss.kind = 'ga_unit'
           AND ss.status = 'sold' AND ss.reservation_id IS NULL)
  FROM ticket_tiers t WHERE t.id = $1`, tierID).Scan(&capacity, &pl.SoldUpstream); err != nil {
		return Places{}, fmt.Errorf("read category: %w", err)
	}
	if capacity != nil {
		pl.Capacity = *capacity
	}
	return pl, nil
}

func readLedger(ctx context.Context, db gen.DBTX, sessionID uuid.UUID) ([2]int32, error) {
	var total *int32
	var sold int32
	err := db.QueryRow(ctx, `SELECT capacity_total, capacity_sold FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`,
		sessionID).Scan(&total, &sold)
	if errors.Is(err, pgx.ErrNoRows) {
		return [2]int32{}, nil
	}
	if err != nil {
		return [2]int32{}, fmt.Errorf("read ledger: %w", err)
	}
	out := [2]int32{0, sold}
	if total != nil {
		out[0] = *total
	}
	return out, nil
}

func (r *runner) projectAfter() {
	for _, s := range r.session {
		var added int32
		for _, tr := range s.report.Tiers {
			n := int32(tr.Tickets) //nolint:gosec // bounded by one file's tickets of one session
			tr.After = tr.Before
			tr.After.Capacity += n
			tr.After.Places += n
			tr.After.Sold += n
			tr.After.SoldUpstream += n
			added += n
		}
		s.report.LedgerAfter = [2]int32{s.report.LedgerBefore[0] + added, s.report.LedgerBefore[1] + added}
	}
}

func (r *runner) readAfter(ctx context.Context) error {
	q := gen.New(r.db)
	for _, s := range r.session {
		for _, tr := range s.report.Tiers {
			pl, err := readPlaces(ctx, r.db, q, s.ID, tr.TierID)
			if err != nil {
				return err
			}
			tr.After = pl
		}
		l, err := readLedger(ctx, r.db, s.ID)
		if err != nil {
			return err
		}
		s.report.LedgerAfter = l
	}
	return nil
}

func (r *runner) sortSessions() {
	for _, s := range r.session {
		r.rep.Sessions = append(r.rep.Sessions, s.report)
	}
	sort.Slice(r.rep.Sessions, func(i, j int) bool {
		a, b := r.rep.Sessions[i], r.rep.Sessions[j]
		if !a.StartAt.Equal(b.StartAt) {
			return a.StartAt.Before(b.StartAt)
		}
		return a.SessionID.String() < b.SessionID.String()
	})
}

// apply writes one order in its own transaction, under the session row lock
// taken as its first statement, and re-checks under that lock what the plan
// decided from.
func (r *runner) apply(ctx context.Context, po *plannedOrder, authority uuid.UUID) (int64, bool, *Problem, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, false, nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	txq := gen.New(tx)
	p := po.p
	sess := po.sess

	// 1. The sessions row lock, before any read this transaction decides from.
	if _, err := txq.IncrementSessionSeatStatusVersion(ctx, sess.ID); err != nil {
		return 0, false, nil, fmt.Errorf("lock session: %w", err)
	}
	// 2. A concurrent run may have filed it meanwhile.
	var linked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wp_order_links WHERE org_id = $1 AND wc_order_id = $2)`,
		r.opt.OrgID, p.WCOrderID).Scan(&linked); err != nil {
		return 0, false, nil, err
	}
	if linked {
		return 0, true, nil, nil
	}

	// 3. The places: added as sold upstream, the category and the ledger grow.
	byTier := map[uuid.UUID][]int{}
	var tierOrder []uuid.UUID
	for i, t := range po.tickets {
		if _, ok := byTier[t.TierID]; !ok {
			tierOrder = append(tierOrder, t.TierID)
		}
		byTier[t.TierID] = append(byTier[t.TierID], i)
	}
	placeOf := make([]gen.GAUnitKey, len(po.tickets))
	for _, tier := range tierOrder {
		idx := byTier[tier]
		places, err := gaquota.AddSoldUpstreamPlaces(ctx, txq, sess.ID, tier, int32(len(idx))) //nolint:gosec // one order's tickets
		if err != nil {
			if errors.Is(err, gaquota.ErrSeatedCategory) {
				return 0, false, &Problem{RefuseNoGACategory, "the category is seated"}, nil
			}
			return 0, false, nil, err
		}
		for k, i := range idx {
			placeOf[i] = places[k]
		}
	}

	// 4. The buyer as a customer of the organization.
	store := customers.NewStoreFromQueries(txq)
	cres, err := customers.Resolve(ctx, store, customers.ResolveInput{
		Email: p.Email, Phone: p.Phone, Name: p.BuyerName, ChannelID: sess.ChannelID,
		DefaultRegion: sess.Country, Locale: p.Locale, Source: customers.SourceImport, Now: r.opt.Now,
	})
	if err != nil {
		return 0, false, nil, fmt.Errorf("resolve customer: %w", err)
	}
	customerID := cres.Customer.ID
	if err := customers.LinkOrg(ctx, store, customerID, r.opt.OrgID, "import"); err != nil {
		return 0, false, nil, fmt.Errorf("link customer: %w", err)
	}

	// 5. Reservation (already converted), checkout (completed), order (paid).
	var tierID *uuid.UUID
	if len(tierOrder) == 1 {
		tierID = &tierOrder[0]
	}
	quantity := len(po.tickets)
	var reservationID uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO reservations (org_id, channel_id, session_id, tier_id, quantity, state, expires_at,
                          created_at, updated_at, converted_at, customer_id)
VALUES ($1, $2, $3, $4, $5, 'converted', $6, $7, now(), $6, $8)
RETURNING id`, r.opt.OrgID, sess.ChannelID, sess.ID, tierID, quantity, p.PaidAt, p.CreatedAt, customerID).Scan(&reservationID); err != nil {
		return 0, false, nil, fmt.Errorf("insert reservation: %w", err)
	}
	var locale *string
	if p.Locale != "" {
		locale = &p.Locale
	}
	var checkoutID uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO checkout_sessions (org_id, channel_id, reservation_id, state, subtotal, discount, platform_fee,
                               provider_fee, tax, total, currency, completed_at, created_at, updated_at, buyer_locale)
VALUES ($1, $2, $3, 'completed', $4, $5, $6, 0, 0, $7, $8, $9, $10, now(), $11)
RETURNING id`, r.opt.OrgID, sess.ChannelID, reservationID, po.subtotal, po.discount, po.charge, po.total,
		sess.Currency, p.PaidAt, p.CreatedAt, locale).Scan(&checkoutID); err != nil {
		return 0, false, nil, fmt.Errorf("insert checkout session: %w", err)
	}
	meta, err := json.Marshal(map[string]any{"legacy_import": map[string]any{
		"system": "bil24", "bil24_order_id": p.Bil24OrderID, "wc_order_id": p.WCOrderID,
		"wc_status": p.Status, "live_ids_missing": p.LiveIDsMissing, "refunded_in_wp": p.Refunded,
	}})
	if err != nil {
		return 0, false, nil, err
	}
	var externalRef *string
	if po.externalOK {
		externalRef = &p.WCOrderID
	}
	var name, phone *string
	if p.BuyerName != "" {
		name = &p.BuyerName
	}
	if p.Phone != "" {
		phone = &p.Phone
	}
	var orderID uuid.UUID
	var systemID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO orders (system_id, org_id, channel_id, event_id, session_id, customer_id, checkout_session_id,
                    reservation_id, external_ref, source, status, currency, subtotal, discount, charge, total,
                    buyer_name, buyer_email, buyer_phone, payment_method, paid_at, metadata, created_at, updated_at)
SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'paid', $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, now()
 WHERE NOT EXISTS (SELECT 1 FROM orders WHERE system_id = $1)
RETURNING id, system_id`,
		p.Bil24OrderID, r.opt.OrgID, sess.ChannelID, sess.EventID, sess.ID, customerID, checkoutID,
		reservationID, externalRef, ordering.SourceLegacyImport, sess.Currency, po.subtotal, po.discount, po.charge, po.total,
		name, p.Email, phone, PaymentMethod, p.PaidAt, meta, p.CreatedAt).Scan(&orderID, &systemID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, &Problem{RefuseOrderNumberTaken, fmt.Sprintf("a platform order already has number %d", p.Bil24OrderID)}, nil
		}
		return 0, false, nil, fmt.Errorf("insert order: %w", err)
	}

	// 6. Tickets (each under its Bil24 number, which MACS reports scans by),
	// order lines, the original barcode and its credential.
	holder := holderName(p.BuyerName)
	ticketNumbers := make([]int64, 0, len(po.tickets))
	for i, t := range po.tickets {
		place := placeOf[i]
		var ticketID uuid.UUID
		var ticketNumber int64
		if err := tx.QueryRow(ctx, `
INSERT INTO tickets (checkout_session_id, session_id, tier_id, holder_email, holder_name, status, issued_at,
                     created_at, updated_at, seat_key, seat_sector, seat_row, seat_number, ordinal, order_id,
                     system_ticket_id)
VALUES ($1, $2, $3, $4, $5, 'active', $6, $6, now(), $7, '', '', '', $8, $9, $10)
RETURNING id, system_ticket_id`, checkoutID, sess.ID, t.TierID, p.Email, holder, p.PaidAt, place.SeatKey, i, orderID,
			t.Bil24TicketID).
			Scan(&ticketID, &ticketNumber); err != nil {
			return 0, false, nil, fmt.Errorf("insert ticket: %w", err)
		}
		ticketNumbers = append(ticketNumbers, ticketNumber)
		if _, err := tx.Exec(ctx, `
INSERT INTO order_items (order_id, ordinal, kind, tier_id, session_seat_id, ticket_id, unit_price, discount, charge, total)
VALUES ($1, $2, 'ticket', $3, $4, $5, $6, $7, $8, $9)`,
			orderID, i, t.TierID, place.ID, ticketID, t.Money.Price, t.Money.Discount, t.Money.Charge, t.Money.Total); err != nil {
			return 0, false, nil, fmt.Errorf("insert order item: %w", err)
		}
		ok, err := txq.InsertBarcodeIfUnique(ctx, authority, t.Barcode, &ticketID)
		if err != nil {
			return 0, false, nil, fmt.Errorf("insert barcode: %w", err)
		}
		if !ok {
			return 0, false, &Problem{RefuseBarcodeTaken, fmt.Sprintf("Bil24 ticket %d: barcode %s is already known to a barcode authority", t.Bil24TicketID, t.Barcode)}, nil
		}
		if _, err := txq.InsertTicketCredential(ctx, ticketID, "ean13", t.Barcode); err != nil {
			return 0, false, nil, fmt.Errorf("insert ean13 credential: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO legacy_ticket_imports (ticket_id, org_id, order_id, bil24_ticket_id, bil24_order_id, barcode, wc_order_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)`, ticketID, r.opt.OrgID, orderID, t.Bil24TicketID, p.Bil24OrderID, t.Barcode, p.WCOrderID); err != nil {
			return 0, false, nil, fmt.Errorf("record imported ticket: %w", err)
		}
	}

	// 7. The order's history line and the WooCommerce link.
	payload, err := json.Marshal(map[string]any{"bil24_order_id": p.Bil24OrderID, "wc_order_id": p.WCOrderID, "tickets": len(po.tickets)})
	if err != nil {
		return 0, false, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_events (order_id, type, actor, payload) VALUES ($1, $2, 'system', $3)`,
		orderID, OrderEventType, payload); err != nil {
		return 0, false, nil, fmt.Errorf("insert order event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO wp_order_links (org_id, wc_order_id, order_id, ticket_system_ids, bil24_order_id)
VALUES ($1, $2, $3, $4, $5)`, r.opt.OrgID, p.WCOrderID, orderID, ticketNumbers, p.Bil24OrderID); err != nil {
		return 0, false, nil, fmt.Errorf("insert wp_order_links: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, nil, fmt.Errorf("commit: %w", err)
	}
	return systemID, false, nil, nil
}

// holderName is the ticket's holder: the buyer, within the column's 200
// characters; nil when there is no name.
func holderName(name string) *string {
	n := strings.TrimSpace(name)
	if n == "" {
		return nil
	}
	if r := []rune(n); len(r) > 200 {
		n = strings.TrimSpace(string(r[:200]))
	}
	return &n
}
