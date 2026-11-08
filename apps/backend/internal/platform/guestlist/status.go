package guestlist

import (
	"sort"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The four MACS holderStatus values (internal/platform/macs/macs.go and
// 08_architecture/17_macs_integration_contract.md): the guest list speaks the
// scanner system's own vocabulary, so the organizer's file and the gate agree.
const (
	StatusValid      = 0 // valid, not yet entered
	StatusCheckedIn  = 1
	StatusCheckedOut = 2
	StatusRefunded   = 3 // refunded, cancelled, revoked or replaced
)

// Why a ticket is status 3 (the "Status reason" column).
const (
	ReasonNone             = ""
	ReasonRefunded         = "refunded"
	ReasonCancelled        = "cancelled"
	ReasonSessionCancelled = "session_cancelled"
	ReasonReissued         = "reissued"
)

// StatusOf maps a ticket onto a MACS holderStatus and, for 3, the reason.
//
//   - status 3, reason reissued: the ticket was replaced because it went to a
//     wrong e-mail address (tickets.status 'transferred', cancellation_reason
//     'reissued', table ticket_reissues);
//   - status 3, reason refunded: a cancelled/revoked ticket that carries a
//     refund (refund_id, refund_date or refund_price);
//   - status 3, reason cancelled: any other non-active ticket;
//   - status 3, reason session_cancelled: an active ticket of a cancelled
//     session (cancelling a session leaves orders paid and tickets active);
//   - otherwise 1 when the ticket was admitted (tickets.used_at, an admitted
//     scan_events row or a scanned barcode), else 0.
//
// 2 (checked out) is never derived: scan_events stores no direction, and
// guessing an exit from the number of scans would mark a guest who was
// simply scanned twice at the door as gone. StatusCheckedOut stays for the
// labels until the scanner reports a direction.
func StatusOf(r gen.GuestListRow) (code int, reason string) {
	if r.TicketStatus != "active" {
		switch {
		case r.Reissued:
			return StatusRefunded, ReasonReissued
		case r.Refunded:
			return StatusRefunded, ReasonRefunded
		default:
			return StatusRefunded, ReasonCancelled
		}
	}
	if r.SessionStatus == "cancelled" {
		return StatusRefunded, ReasonSessionCancelled
	}
	if r.EnteredAt != nil || r.MacsAdmittedScans > 0 {
		return StatusCheckedIn, ReasonNone
	}
	return StatusValid, ReasonNone
}

var statusLabels = map[string][4]string{
	"en": {"Valid", "Checked in", "Checked out", "Refunded / cancelled"},
	"ru": {"Действителен", "Вошёл", "Вышел", "Возврат / отмена"},
	"es": {"Válida", "Ha entrado", "Ha salido", "Devuelta / cancelada"},
}

var reasonLabels = map[string]map[string]string{
	"en": {ReasonRefunded: "Refunded", ReasonCancelled: "Cancelled", ReasonSessionCancelled: "Session cancelled",
		ReasonReissued: "Replaced: sent to a wrong e-mail address"},
	"ru": {ReasonRefunded: "Возврат", ReasonCancelled: "Отменён", ReasonSessionCancelled: "Сеанс отменён",
		ReasonReissued: "Заменён: билет ушёл на неверный e-mail"},
	"es": {ReasonRefunded: "Devuelta", ReasonCancelled: "Cancelada", ReasonSessionCancelled: "Sesión cancelada",
		ReasonReissued: "Sustituida: enviada a un e-mail equivocado"},
}

// StatusLabel is the status as the file prints it: the words and, in
// brackets, the MACS code, "Checked in (1)".
func StatusLabel(locale string, code int) string {
	l, ok := statusLabels[locale]
	if !ok {
		l = statusLabels["en"]
	}
	if code < 0 || code > 3 {
		code = StatusRefunded
	}
	return l[code] + " (" + string(rune('0'+code)) + ")"
}

// ReasonLabel is the reason in locale; empty for no reason.
func ReasonLabel(locale, reason string) string {
	if reason == "" {
		return ""
	}
	m, ok := reasonLabels[locale]
	if !ok {
		m = reasonLabels["en"]
	}
	return m[reason]
}

// SplitName splits a stored full name ("orders.buyer_name" and the ticket's
// holder_name are ONE free-text field, there is no first/last pair anywhere)
// at the LAST space: everything before it is the first name, the last word is
// the last name. A single word is a last name, so sorting by that column
// keeps it. "Maria del Carmen Lopez" splits as "Maria del Carmen" / "Lopez";
// a compound surname is split wrongly, which is the price of one field.
func SplitName(full string) (last, first string) {
	f := strings.Join(strings.Fields(full), " ")
	i := strings.LastIndexByte(f, ' ')
	if i < 0 {
		return f, ""
	}
	return f[i+1:], f[:i]
}

// sortRows orders rows by session start, then last name, first name (empty
// names last) and ticket number. The SQL already groups by session; the
// guest order is decided here because only Go knows the split.
func sortRows(rows []gen.GuestListRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if !a.SessionStartAt.Equal(b.SessionStartAt) {
			return a.SessionStartAt.Before(b.SessionStartAt)
		}
		if a.SessionID != b.SessionID {
			return a.SessionID.String() < b.SessionID.String()
		}
		la, fa := SplitName(a.GuestName)
		lb, fb := SplitName(b.GuestName)
		if (la == "") != (lb == "") {
			return lb == ""
		}
		if la, lb := strings.ToLower(la), strings.ToLower(lb); la != lb {
			return la < lb
		}
		if fa, fb := strings.ToLower(fa), strings.ToLower(fb); fa != fb {
			return fa < fb
		}
		return a.SystemTicketID < b.SystemTicketID
	})
}
