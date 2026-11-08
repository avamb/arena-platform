package salesalert

// reissue_alerts.go — the old code of a replaced ticket (spec 63 section 4.8,
// owner decision of 2026-11-08, О-5). Two notices, each once per old ticket:
//
//   - the old code was already used at the door when the ticket was replaced
//     (ticket_reissue_items.old_used_at, also filled late by an offline MACS
//     batch);
//   - somebody tried to enter with the old code after the replacement
//     (old_code_attempt_*, written by the arena scanner and the MACS scan
//     intake through ticketreissue.RecordOldCodeAttempt).
//
// Three places: the order card shows it from the reissue history, the
// organization's own sales chats get a message in the organization's
// language, and without a chat the owners get an e-mail. The item is claimed
// before anything is sent, so a crash loses a message and never repeats it.
// No buyer address is ever in the message.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// KindReissueOldCode is the e-mail variant of the two notices.
const KindReissueOldCode = "reissue_old_code"

// reissueBatch bounds one pass of each notice.
const reissueBatch = 200

// ReissueNotice is what one notice says.
type ReissueNotice struct {
	// Attempt: a try after the replacement; false: an entry before it.
	Attempt     bool
	OrderNumber int64
	OldNumber   int64
	NewNumber   int64
	At          time.Time
	// Source / Result / Gate of the try (Attempt only).
	Source string
	Result string
	Gate   string
}

type reissueItem struct {
	reissueID, oldTicketID, orgID, sessionID uuid.UUID
	notice                                   ReissueNotice
}

func (w *Watcher) reissueOldCodes(ctx context.Context, now time.Time, silent bool) (int, error) {
	told := 0
	for _, attempt := range []bool{false, true} {
		n, err := w.reissueNotices(ctx, now, silent, attempt)
		told += n
		if err != nil {
			return told, err
		}
	}
	return told, nil
}

func (w *Watcher) reissueNotices(ctx context.Context, now time.Time, silent, attempt bool) (int, error) {
	at, notified, where := "i.old_used_at", "entry_notified_at", "i.old_used_at IS NOT NULL AND i.entry_notified_at IS NULL"
	if attempt {
		at, notified, where = "i.old_code_attempt_at", "attempt_notified_at", "i.old_code_attempt_at IS NOT NULL AND i.attempt_notified_at IS NULL"
	}
	rows, err := w.pool.Query(ctx, `
		SELECT i.reissue_id, i.old_ticket_id, r.org_id, ot.session_id,
		       COALESCE(o.system_id, 0), ot.system_ticket_id, nt.system_ticket_id, `+at+`,
		       COALESCE(i.old_code_attempt_source, ''), COALESCE(i.old_code_attempt_result, ''),
		       COALESCE(i.old_code_attempt_gate, '')
		FROM   ticket_reissue_items i
		JOIN   ticket_reissues r ON r.id = i.reissue_id
		JOIN   tickets ot ON ot.id = i.old_ticket_id
		JOIN   tickets nt ON nt.id = i.new_ticket_id
		LEFT   JOIN orders o ON o.id = r.order_id
		WHERE  `+where+`
		ORDER  BY `+at+`
		LIMIT  $1`, reissueBatch)
	if err != nil {
		return 0, err
	}
	var items []reissueItem
	for rows.Next() {
		var it reissueItem
		n := &it.notice
		if err := rows.Scan(&it.reissueID, &it.oldTicketID, &it.orgID, &it.sessionID,
			&n.OrderNumber, &n.OldNumber, &n.NewNumber, &n.At, &n.Source, &n.Result, &n.Gate); err != nil {
			rows.Close()
			return 0, err
		}
		n.Attempt = attempt
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	told := 0
	for _, it := range items {
		tag, err := w.pool.Exec(ctx, `UPDATE ticket_reissue_items SET `+notified+` = $3
			WHERE reissue_id = $1 AND old_ticket_id = $2 AND `+notified+` IS NULL`, it.reissueID, it.oldTicketID, now)
		if err != nil {
			return told, err
		}
		if tag.RowsAffected() == 0 || silent {
			continue
		}
		told++
		w.tellReissue(ctx, it)
	}
	return told, nil
}

// tellReissue posts to the organization's chats, else mails the owners.
func (w *Watcher) tellReissue(ctx context.Context, it reissueItem) {
	in, err := LoadSessionInfo(ctx, w.pool, it.orgID, it.sessionID)
	if err != nil {
		w.logger.Warn("sales state watch: reissue notice without its session",
			"reissue_id", it.reissueID.String(), "error", err.Error())
		return
	}
	if w.announcer != nil {
		_, delivered, aerr := w.announcer.AnnounceToOrg(ctx, it.orgID.String(), ReissueOldCodeText(in, it.notice))
		if aerr != nil {
			w.logger.Warn("sales state watch: reissue chat message", "reissue_id", it.reissueID.String(), "error", aerr.Error())
		}
		if delivered > 0 {
			return
		}
	}
	n := it.notice
	p := AlertEmailPayload{
		Kind: KindReissueOldCode, OrgID: it.orgID.String(), SessionID: it.sessionID.String(),
		Attempt: n.Attempt, OrderNumber: n.OrderNumber, OldNumber: n.OldNumber, NewNumber: n.NewNumber,
		At: n.At.UTC().Format(time.RFC3339), Source: n.Source, Result: n.Result, Gate: n.Gate,
	}
	if err := enqueueEmail(ctx, w.pool, p); err != nil {
		w.logger.Warn("sales state watch: reissue e-mail not queued", "reissue_id", it.reissueID.String(), "error", err.Error())
	}
}

// InZone prints t on the session venue's clock (Madrid when it has none).
func (in SessionInfo) InZone(t time.Time) string {
	loc, err := time.LoadLocation(in.Timezone)
	if in.Timezone == "" || err != nil {
		if loc, err = time.LoadLocation("Europe/Madrid"); err != nil {
			return t.UTC().Format("02.01.2006 15:04") + " UTC" // allow:timeformat
		}
	}
	return t.In(loc).Format("02.01.2006 15:04") // allow:timeformat
}

type reissueTexts struct {
	entryTitle, attemptTitle            string
	order, replaced, entry, attempt     string
	admitted, denied, newValid, sources string
	gate                                string
}

var reissueTextsByLocale = map[string]reissueTexts{
	"en": {
		entryTitle:   "🎫 <b>Replaced ticket: the old code was already used</b>",
		attemptTitle: "⚠️ <b>Someone tried to enter with an old code</b>",
		order:        "Order %d.",
		replaced:     "Ticket №%d was replaced by ticket №%d.",
		entry:        "The old code was used at the entrance on %s, before the replacement. The new ticket №%d is valid too.",
		attempt:      "The old code was scanned on %s (%s).",
		admitted:     "The scanner let the person in.",
		denied:       "The scanner did not let the person in.",
		newValid:     "The new ticket №%d is valid.",
		sources:      "arena_scanner=Arena scanner;macs=MACS",
		gate:         "entrance %s",
	},
	"ru": {
		entryTitle:   "🎫 <b>Заменённый билет: по старому коду уже был вход</b>",
		attemptTitle: "⚠️ <b>Попытка войти по старому коду</b>",
		order:        "Заказ %d.",
		replaced:     "Билет №%d заменён на билет №%d.",
		entry:        "По старому коду был вход %s, до замены. Новый билет №%d тоже действует.",
		attempt:      "Старый код отсканирован %s (%s).",
		admitted:     "Сканер пропустил человека.",
		denied:       "Сканер не пропустил человека.",
		newValid:     "Новый билет №%d действует.",
		sources:      "arena_scanner=сканер Arena;macs=MACS",
		gate:         "вход %s",
	},
	"es": {
		entryTitle:   "🎫 <b>Entrada reemplazada: el código anterior ya se usó</b>",
		attemptTitle: "⚠️ <b>Alguien intentó entrar con un código anterior</b>",
		order:        "Pedido %d.",
		replaced:     "La entrada №%d se reemplazó por la entrada №%d.",
		entry:        "El código anterior se usó en la entrada el %s, antes del reemplazo. La nueva entrada №%d también es válida.",
		attempt:      "El código anterior se escaneó el %s (%s).",
		admitted:     "El escáner dejó pasar a la persona.",
		denied:       "El escáner no dejó pasar a la persona.",
		newValid:     "La nueva entrada №%d es válida.",
		sources:      "arena_scanner=escáner de Arena;macs=MACS",
		gate:         "acceso %s",
	},
}

func reissueTextsFor(locale string) reissueTexts {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if t, ok := reissueTextsByLocale[l]; ok {
		return t
	}
	return reissueTextsByLocale["en"]
}

func (t reissueTexts) source(code string) string {
	for _, pair := range strings.Split(t.sources, ";") {
		if k, v, ok := strings.Cut(pair, "="); ok && k == code {
			return v
		}
	}
	return code
}

// ReissueOldCodeText is the chat message of a notice (HTML).
func ReissueOldCodeText(in SessionInfo, n ReissueNotice) string {
	t := reissueTextsFor(in.OrgLocale)
	var b strings.Builder
	if n.Attempt {
		b.WriteString(t.attemptTitle)
	} else {
		b.WriteString(t.entryTitle)
	}
	writeSession(&b, in)
	if n.OrderNumber > 0 {
		fmt.Fprintf(&b, t.order, n.OrderNumber)
		b.WriteString(" ")
	}
	fmt.Fprintf(&b, t.replaced, n.OldNumber, n.NewNumber)
	b.WriteString("\n")
	if !n.Attempt {
		fmt.Fprintf(&b, t.entry, esc(in.InZone(n.At)), n.NewNumber)
		return b.String()
	}
	where := t.source(n.Source)
	if g := strings.TrimSpace(n.Gate); g != "" {
		where += ", " + fmt.Sprintf(t.gate, g)
	}
	fmt.Fprintf(&b, t.attempt, esc(in.InZone(n.At)), esc(where))
	b.WriteString(" ")
	if n.Result == "admitted" {
		b.WriteString(t.admitted)
	} else {
		b.WriteString(t.denied)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, t.newValid, n.NewNumber)
	return b.String()
}
