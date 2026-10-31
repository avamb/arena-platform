package sessionchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobTypeChangeEmail is the worker_jobs.job_type of the letter Arena itself
// sends to a buyer whose session moved or was cancelled.
const JobTypeChangeEmail = "session.change_email"

// changeEmailMaxAttempts is the retry budget of one letter job.
const changeEmailMaxAttempts = 6

// MaxMessageRunes bounds the organizer's own text in a letter: the visible
// characters of the session_message profile (richtext.ProfileSessionMessage).
const MaxMessageRunes = 1000

// Notice states of session_change_notices.state.
const (
	StateQueued          = "queued"
	StateDeliveredToSite = "delivered_to_site"
	StateSent            = "sent"
	StateFailed          = "failed"
	StateSkipped         = "skipped"
)

// Routes of session_change_notices.route.
const (
	// RouteSite is an order whose selling channel has a WordPress site: that
	// site writes to its own buyer, from its own domain.
	RouteSite = "site"
	// RouteArena is every other order: Arena writes the letter itself.
	RouteArena = "arena"
)

// Blocked reasons reported by Preview and answered by Apply.
const (
	BlockedContactMissing = "contact_missing"
	BlockedSiteRoute      = "site_route_unsupported"
)

var (
	// ErrContactMissing means the change touches buyers but the event has no
	// organizer e-mail for them to answer to (decision 3 of the spec).
	ErrContactMissing = errors.New("sessionchange: the organizer has no contact e-mail")
	// ErrSiteRoute means at least one affected order was sold through a
	// WordPress site, whose own letter is not wired yet; the move is refused
	// rather than sent without it (decision 2: no move without a letter).
	ErrSiteRoute = errors.New("sessionchange: an affected order was sold through a site that cannot be notified yet")
	// ErrMessageTooLong means the organizer's text exceeds MaxMessageRunes
	// visible characters (or the 4000 characters of its HTML).
	ErrMessageTooLong = errors.New("sessionchange: the message is too long")
	// ErrMessageInvalid means the organizer's text is refused for another
	// reason (forbidden characters, too many links, ...); MessageReason says which.
	ErrMessageInvalid = errors.New("sessionchange: the message is not acceptable")
)

// DB is the slice of pgx.Tx / *pgxpool.Pool the package needs.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Session is a session row's identity plus its buyer-visible state.
type Session struct {
	ID      uuid.UUID
	EventID uuid.UUID
	OrgID   uuid.UUID
	State   State
}

const loadSessionSQL = `
SELECT s.id, s.event_id, e.org_id, s.start_at, s.end_at, s.venue_id,
       COALESCE(v.name, ''), COALESCE(v.timezone, ''), s.status, s.deleted_at IS NOT NULL
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  s.id = $1`

// Load reads a session, deleted or not. With lock set it takes the row lock
// first, which every inventory-touching transaction takes anyway (AGENTS.md:
// the sessions row lock is step one).
func Load(ctx context.Context, db DB, sessionID uuid.UUID, lock bool) (Session, error) {
	q := loadSessionSQL
	if lock {
		q += " FOR UPDATE OF s"
	}
	var s Session
	err := db.QueryRow(ctx, q, sessionID).Scan(&s.ID, &s.EventID, &s.OrgID,
		&s.State.StartAt, &s.State.EndAt, &s.State.VenueID,
		&s.State.VenueName, &s.State.Timezone, &s.State.Status, &s.State.Deleted)
	return s, err
}

// Notice is what the caller knows about who is changing the session and what
// they want the buyers to read.
type Notice struct {
	// Message is the organizer's own text, shown to the buyer under the
	// standard wording. May be empty.
	Message string
	// MessageFormat says how Message is written: richtext.FormatText (the
	// default, also for "") or richtext.FormatART.
	MessageFormat string
	// ActorType is "user", "api_key" or "system"; ActorID is free text.
	ActorType string
	ActorID   string
}

// NormalizeMessage trims the organizer's text, unifies line breaks, drops
// control characters other than newline and bounds its length.
func NormalizeMessage(raw string) (string, error) {
	out := strings.TrimSpace(cleanPlainMessage(raw))
	if utf8.RuneCountInString(out) > MaxMessageRunes {
		return "", ErrMessageTooLong
	}
	return out, nil
}

// MessageError is the refusal of an organizer's text that is not just too long:
// Reason is the short stable word of richtext (control_characters,
// too_many_links, ...). errors.Is(err, ErrMessageInvalid) is true for it.
type MessageError struct{ Reason string }

func (e *MessageError) Error() string {
	return "sessionchange: the message is not acceptable: " + e.Reason
}

// Is makes errors.Is(err, ErrMessageInvalid) true.
func (e *MessageError) Is(target error) bool { return target == ErrMessageInvalid }

// MessageReason is the short word of a refused message, "" for any other error.
func MessageReason(err error) string {
	var me *MessageError
	if errors.As(err, &me) {
		return me.Reason
	}
	return ""
}

// NormalizeNotice is the one write path of the organizer's text (spec 44,
// RT-11). format is richtext.FormatText (also "": the old plain text, which
// every client that sends no format keeps writing, the Telegram bot among
// them) or richtext.FormatART; anything else is a *MessageError (reason
// invalid_format). It returns the text to store and the format to store next
// to it:
//
//   - art: richtext.Normalize with the session_message profile; the stored
//     text is ART and the format "art";
//   - text: the plain text exactly as before (trimmed, line breaks unified,
//     control characters other than newline and tab dropped, at most
//     MaxMessageRunes characters), stored as it is with the format "text", so
//     the bot's journal rows are the ones it always wrote. A character that
//     richtext.Render drops (a direction override, a tag character of the
//     England flag, a C1 control) is stripped silently, never refused: older
//     clients sent them. The letters read the text through richtext.Render,
//     which converts it on the fly.
//
// "Too long" is ErrMessageTooLong. For art any other refusal is a
// *MessageError with the short word of richtext; a text is only ever too long.
// An empty text stays empty.
func NormalizeNotice(raw, format string) (stored, storedFormat string, err error) {
	switch format {
	case "", richtext.FormatText:
		// Never refused for a character an older client sent: cleanPlainMessage
		// strips what Render strips. The length of the HTML Render makes of it
		// is not checked either (900 "&" are 900 characters of text, not 4500).
		plain := strings.TrimSpace(cleanPlainMessage(raw))
		if utf8.RuneCountInString(plain) > MaxMessageRunes {
			return "", "", ErrMessageTooLong
		}
		return plain, richtext.FormatText, nil
	case richtext.FormatART:
		out, err := richtext.Normalize(raw, richtext.FormatART, richtext.ProfileSessionMessage)
		if err != nil {
			return "", "", noticeError(err)
		}
		return out, richtext.FormatART, nil
	}
	return "", "", &MessageError{Reason: string(richtext.ReasonInvalidFormat)}
}

// noticeError maps a richtext refusal: a text over a limit is ErrMessageTooLong
// (the existing 422 session.change_message_too_long), anything else a *MessageError.
func noticeError(err error) error {
	var re *richtext.Error
	if errors.As(err, &re) && (re.Reason == richtext.ReasonTooLong || re.Reason == richtext.ReasonHTMLTooLong) {
		return ErrMessageTooLong
	}
	return &MessageError{Reason: string(richtext.ReasonOf(err))}
}

// cleanPlainMessage unifies line breaks and drops control characters other
// than newline and tab, and strips what richtext.Render strips.
func cleanPlainMessage(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var b strings.Builder
	for _, r := range raw {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return richtext.StripRefused(b.String())
}

// Contact is who a buyer writes to when the new date does not suit them.
type Contact struct {
	// Source is "promoter" or "organization"; empty when neither has an e-mail.
	Source      string    `json:"source"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Phone       string    `json:"phone"`
	PhoneHidden bool      `json:"phone_hidden"`
	TargetKind  string    `json:"target_kind"`
	TargetID    uuid.UUID `json:"target_id"`
	TargetName  string    `json:"target_name"`
}

// Complete reports whether buyers have an address to write to.
func (c Contact) Complete() bool { return strings.TrimSpace(c.Email) != "" }

// PublicPhone is the phone number a letter may print: empty when the owner
// hid it.
func (c Contact) PublicPhone() string {
	if c.PhoneHidden {
		return ""
	}
	return strings.TrimSpace(c.Phone)
}

const promoterContactSQL = `
SELECT p.id, p.name, COALESCE(btrim(p.email), ''), COALESCE(btrim(p.phone), ''), p.phone_hidden
FROM   event_promoters ep
JOIN   org_promoters   p ON p.id = ep.promoter_id AND p.org_id = ep.org_id
WHERE  ep.event_id = $1`

const orgContactSQL = `
SELECT o.id, o.name, COALESCE(btrim(o.contact_email), ''), COALESCE(btrim(o.contact_phone), ''), o.contact_phone_hidden
FROM   organizations o
WHERE  o.id = $1`

// ResolveContact picks the contact of an event: its promoter when the
// promoter has an e-mail, otherwise the organization (owner decision
// 2026-10-02). When neither has one the result is incomplete and names the
// row a client should fill in: the promoter if the event has one, else the
// organization.
func ResolveContact(ctx context.Context, db DB, eventID, orgID uuid.UUID) (Contact, error) {
	var promo Contact
	havePromoter := false
	err := db.QueryRow(ctx, promoterContactSQL, eventID).
		Scan(&promo.TargetID, &promo.Name, &promo.Email, &promo.Phone, &promo.PhoneHidden)
	switch {
	case err == nil:
		havePromoter = true
		promo.TargetKind = "promoter"
		promo.TargetName = promo.Name
		promo.Source = "promoter"
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return Contact{}, fmt.Errorf("sessionchange: promoter contact: %w", err)
	}
	if havePromoter && promo.Complete() {
		return promo, nil
	}

	var org Contact
	if err := db.QueryRow(ctx, orgContactSQL, orgID).
		Scan(&org.TargetID, &org.Name, &org.Email, &org.Phone, &org.PhoneHidden); err != nil {
		return Contact{}, fmt.Errorf("sessionchange: organization contact: %w", err)
	}
	org.TargetKind = "organization"
	org.TargetName = org.Name
	org.Source = "organization"
	if org.Complete() {
		return org, nil
	}
	// Nobody has an address: tell the client which row to complete.
	if havePromoter {
		promo.Source = ""
		return promo, nil
	}
	org.Source = ""
	return org, nil
}

// AffectedOrder is one order a change touches.
type AffectedOrder struct {
	OrderID   uuid.UUID
	ChannelID uuid.UUID
	// Email is the address the letter goes to: the buyer's, else a ticket
	// holder's, else (a staff invitation without a guest address) the issuer's.
	Email   string
	Tickets int
	HasSite bool
	// ToIssuer is true when Email is the issuing staff member's address because
	// the guest of the invitation left none.
	ToIssuer bool
}

const affectedOrdersSQL = `
SELECT x.id, x.channel_id,
       CASE WHEN x.guest <> '' THEN x.guest ELSE x.issuer END AS email,
       x.tickets,
       x.has_site,
       (x.guest = '' AND x.issuer <> '') AS to_issuer
FROM (
  SELECT o.id, o.channel_id, o.created_at,
         COALESCE(NULLIF(btrim(o.buyer_email), ''),
                  (SELECT btrim(t2.holder_email) FROM tickets t2
                    WHERE t2.order_id = o.id AND t2.holder_email IS NOT NULL
                      AND btrim(t2.holder_email) <> '' LIMIT 1),
                  '') AS guest,
         COALESCE(` + issuerEmailSQL + `, '') AS issuer,
         COUNT(t.id)::int AS tickets,
         EXISTS (SELECT 1 FROM webhook_subscribers ws
                  WHERE ws.channel_id = o.channel_id AND ws.kind = 'bil24_wp' AND ws.active) AS has_site
  FROM   orders o
  JOIN   tickets t ON t.order_id = o.id AND t.session_id = o.session_id AND t.status = 'active'
  WHERE  o.session_id = $1
    AND  o.status IN ('paid', 'partially_refunded')
  GROUP  BY o.id, o.channel_id, o.buyer_email, o.created_at, o.issued_by, o.source
) x
ORDER  BY x.created_at, x.id`

// AffectedOrders lists the orders whose buyers a change of the session must
// reach: paid or partially refunded, with at least one ticket still active.
// Invitations are included — the guest holds a ticket too.
func AffectedOrders(ctx context.Context, db DB, sessionID uuid.UUID) ([]AffectedOrder, error) {
	rows, err := db.Query(ctx, affectedOrdersSQL, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sessionchange: affected orders: %w", err)
	}
	defer rows.Close()
	var out []AffectedOrder
	for rows.Next() {
		var a AffectedOrder
		if err := rows.Scan(&a.OrderID, &a.ChannelID, &a.Email, &a.Tickets, &a.HasSite, &a.ToIssuer); err != nil {
			return nil, fmt.Errorf("sessionchange: scan affected order: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Impact is the dry-run answer: what saving would do, before anything is
// written.
type Impact struct {
	Kinds       []string `json:"kinds"`
	Orders      int      `json:"orders"`
	Tickets     int      `json:"tickets"`
	ArenaOrders int      `json:"arena_orders"`
	SiteOrders  int      `json:"site_orders"`
	// NoAddress counts the Arena-route orders with no recipient address;
	// their buyers cannot be written to.
	NoAddress int `json:"no_address"`
	// InvitationsToIssuer counts the staff invitations among the Arena-route
	// orders that have no guest e-mail: their letter goes to the staff member
	// who issued them, not to the guest.
	InvitationsToIssuer int     `json:"invitations_to_issuer"`
	Contact             Contact `json:"contact"`
	// Blocked is "" when the change may be saved, else why not.
	Blocked string `json:"blocked"`
}

// Preview computes the Impact of moving the session from its current state to
// proposed, without writing anything.
func Preview(ctx context.Context, db DB, sess Session, proposed State) (Impact, error) {
	imp := Impact{Kinds: Kinds(sess.State, proposed)}
	if imp.Kinds == nil {
		imp.Kinds = []string{}
	}
	contact, err := ResolveContact(ctx, db, sess.EventID, sess.OrgID)
	if err != nil {
		return Impact{}, err
	}
	imp.Contact = contact
	if len(imp.Kinds) == 0 {
		return imp, nil
	}
	orders, err := AffectedOrders(ctx, db, sess.ID)
	if err != nil {
		return Impact{}, err
	}
	for _, o := range orders {
		imp.Orders++
		imp.Tickets += o.Tickets
		if o.HasSite {
			imp.SiteOrders++
			continue
		}
		imp.ArenaOrders++
		switch {
		case o.ToIssuer:
			imp.InvitationsToIssuer++
		case o.Email == "":
			imp.NoAddress++
		}
	}
	switch {
	case imp.Orders == 0:
	case imp.SiteOrders > 0:
		imp.Blocked = BlockedSiteRoute
	case !contact.Complete():
		imp.Blocked = BlockedContactMissing
	}
	return imp, nil
}

// Input is everything Apply needs besides the transaction.
type Input struct {
	SessionID uuid.UUID
	// Old is the state read (with Load) BEFORE the caller's UPDATE.
	Old    State
	Notice Notice
}

// Result reports what Apply did.
type Result struct {
	// ChangeID is uuid.Nil when the save changed nothing a buyer can see.
	ChangeID uuid.UUID
	Kinds    []string
	Orders   int
	Tickets  int
	// Queued is the number of letter jobs put on the queue.
	Queued int
}

const insertChangeSQL = `
INSERT INTO session_changes
       (session_id, event_id, org_id, kinds, old_state, new_state, message, message_format,
        actor_type, actor_id, orders_total, tickets_total)
VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11, $12)
RETURNING id`

const insertNoticeSQL = `
INSERT INTO session_change_notices (change_id, order_id, route, state)
VALUES ($1, $2, $3, 'queued')
ON CONFLICT (change_id, order_id) DO NOTHING`

// Apply is called by every write path INSIDE its transaction, after the
// session row has been updated (or soft-deleted): it reads the new state,
// compares it with Old, and — when a buyer can see the difference — journals
// the change and queues one letter job per affected order. It refuses
// (ErrContactMissing / ErrSiteRoute) when buyers would be left without a
// letter or without someone to answer to; the caller must then roll back, so
// the session does not move.
func Apply(ctx context.Context, tx DB, in Input) (Result, error) {
	message, messageFormat, err := NormalizeNotice(in.Notice.Message, in.Notice.MessageFormat)
	if err != nil {
		return Result{}, err
	}
	sess, err := Load(ctx, tx, in.SessionID, false)
	if err != nil {
		return Result{}, fmt.Errorf("sessionchange: reload session: %w", err)
	}
	kinds := Kinds(in.Old, sess.State)
	if len(kinds) == 0 {
		return Result{}, nil
	}
	orders, err := AffectedOrders(ctx, tx, sess.ID)
	if err != nil {
		return Result{}, err
	}
	var tickets int
	for _, o := range orders {
		tickets += o.Tickets
		if o.HasSite {
			return Result{}, ErrSiteRoute
		}
	}
	if len(orders) > 0 {
		contact, err := ResolveContact(ctx, tx, sess.EventID, sess.OrgID)
		if err != nil {
			return Result{}, err
		}
		if !contact.Complete() {
			return Result{}, ErrContactMissing
		}
	}

	oldJSON, err := json.Marshal(in.Old)
	if err != nil {
		return Result{}, fmt.Errorf("sessionchange: encode old state: %w", err)
	}
	newJSON, err := json.Marshal(sess.State)
	if err != nil {
		return Result{}, fmt.Errorf("sessionchange: encode new state: %w", err)
	}
	actorType := in.Notice.ActorType
	if actorType == "" {
		actorType = "system"
	}
	var changeID uuid.UUID
	if err := tx.QueryRow(ctx, insertChangeSQL, sess.ID, sess.EventID, sess.OrgID, kinds,
		string(oldJSON), string(newJSON), message, messageFormat, actorType, in.Notice.ActorID,
		len(orders), tickets).Scan(&changeID); err != nil {
		return Result{}, fmt.Errorf("sessionchange: journal change: %w", err)
	}

	res := Result{ChangeID: changeID, Kinds: kinds, Orders: len(orders), Tickets: tickets}
	for _, o := range orders {
		if _, err := tx.Exec(ctx, insertNoticeSQL, changeID, o.OrderID, RouteArena); err != nil {
			return Result{}, fmt.Errorf("sessionchange: notice row: %w", err)
		}
		if _, err := worker.EnqueueInTx(ctx, tx, JobTypeChangeEmail, ChangeEmailPayload{
			ChangeID: changeID.String(),
			OrderID:  o.OrderID.String(),
		}, changeEmailMaxAttempts); err != nil {
			return Result{}, fmt.Errorf("sessionchange: enqueue letter: %w", err)
		}
		res.Queued++
	}
	return res, nil
}

// ChangeEmailPayload is the JSON payload of a session.change_email job.
type ChangeEmailPayload struct {
	ChangeID string `json:"change_id"`
	OrderID  string `json:"order_id"`
}

// NoticeFor builds the Notice for a request: the organizer's plain text plus
// the principal behind ctx (a user, or an organization API key).
func NoticeFor(ctx context.Context, message string) Notice {
	return NoticeForFormat(ctx, message, richtext.FormatText)
}

// NoticeForFormat is NoticeFor for a text written in the given format
// (richtext.FormatText or richtext.FormatART).
func NoticeForFormat(ctx context.Context, message, format string) Notice {
	n := Notice{Message: message, MessageFormat: format, ActorType: "system"}
	if actor, ok := auth.ActorFromContext(ctx); ok {
		n.ActorID = actor.ID
		if actor.IsService() {
			n.ActorType = "api_key"
		} else {
			n.ActorType = "user"
		}
	}
	return n
}
