package eventbot

// invite_recipients.go — the recipients step of "Invitations" (EC-12, spec 35
// §6.3): the organizer types who the free tickets are for, one guest per line,
// either "Name, e-mail" or just the e-mail. ParseInviteRecipients is a pure
// function so every rule below is unit-tested without a bot:
//
//   - blank lines are skipped; a leading list marker ("1.", "2)", "-", "*",
//     "•") is dropped, because lists are pasted from notes and spreadsheets;
//   - the e-mail is the ONE word of the line that contains "@" (any order,
//     any of space, comma, semicolon, tab or "<...>" around it); the rest of
//     the line is the name. A line with no "@" word or with two or more is a
//     problem, never a guess;
//   - the e-mail is lower-cased and checked with the same rule the API
//     applies (validResendEmail); the name is at most inviteMaxNameRunes;
//   - the same e-mail twice (in one message, or against the lines already
//     accepted) is refused: one invitation per person per operation, which is
//     what stops a pasted list with a doubled line from mailing a guest twice;
//   - ONE problem line refuses the WHOLE message: the caller keeps nothing
//     from it, so the organizer fixes the lines and sends the message again.
//
// Nothing here logs: the lines are guests' contact details.

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// inviteMaxNameRunes is the longest guest name the bot takes (the API
	// accepts 200; a name that does not fit on a ticket helps no one).
	inviteMaxNameRunes = 100
	// inviteMaxOperation is the most tickets of one operation (the API's own
	// limit, htickets.MaxComplimentaryQty): more goes in several operations.
	inviteMaxOperation = 50
)

// Problem reasons of a line (the keys are bot.inv.reason_<reason>).
const (
	inviteReasonEmail   = "email"   // no usable e-mail address on the line
	inviteReasonName    = "name"    // the name is too long
	inviteReasonRepeat  = "repeat"  // the e-mail is already on the list
	inviteReasonSeveral = "several" // more than one address: one guest per line
)

// InviteRecipient is one guest of an operation.
type InviteRecipient struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// InviteLineProblem is one line that was not accepted.
type InviteLineProblem struct {
	Line   int    // 1-based line number in the message
	Text   string // the line as typed, trimmed (the caller cuts it for display)
	Reason string // one of the inviteReason* constants
}

var inviteListMarker = regexp.MustCompile(`^\s*(?:\d{1,3}[.)]|[-*•–—])\s+`)

// ParseInviteRecipients reads a message. existing are the guests already
// accepted in earlier messages of the same operation (their e-mails count as
// taken). It returns the guests of the message, or — when any line is a
// problem — nothing but the problems.
func ParseInviteRecipients(text string, existing []InviteRecipient) (ok []InviteRecipient, bad []InviteLineProblem) {
	taken := make(map[string]bool, len(existing))
	for _, r := range existing {
		taken[strings.ToLower(r.Email)] = true
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
		if line == "" {
			continue
		}
		rec, reason := parseInviteLine(line)
		if reason == "" {
			key := strings.ToLower(rec.Email)
			if taken[key] {
				reason = inviteReasonRepeat
			} else {
				taken[key] = true
			}
		}
		if reason != "" {
			bad = append(bad, InviteLineProblem{Line: i + 1, Text: line, Reason: reason})
			continue
		}
		ok = append(ok, rec)
	}
	if len(bad) > 0 {
		return nil, bad
	}
	return ok, nil
}

// parseInviteLine reads one non-empty line.
func parseInviteLine(line string) (InviteRecipient, string) {
	line = strings.TrimSpace(inviteListMarker.ReplaceAllString(line, ""))
	// Separators are interchangeable: commas, semicolons, tabs and the angle
	// brackets of "Name <e-mail>" all become spaces for tokenising.
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ';' || r == '\t' || r == '<' || r == '>' || unicode.IsSpace(r)
	})
	var addr string
	found := 0
	for _, f := range fields {
		if strings.Contains(f, "@") {
			found++
			addr = f
		}
	}
	switch {
	case found == 0:
		return InviteRecipient{}, inviteReasonEmail
	case found > 1:
		return InviteRecipient{}, inviteReasonSeveral
	}
	email := strings.ToLower(strings.Trim(addr, `"'()[].:`))
	if !validResendEmail(email) {
		return InviteRecipient{}, inviteReasonEmail
	}
	// The name is the line without the address word and its separators.
	name := strings.Replace(line, addr, " ", 1)
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool {
		return r == ',' || r == ';' || r == '\t' || r == '<' || r == '>' || unicode.IsSpace(r)
	}), " ")
	name = strings.Trim(name, `"'«»“”-–—:|`)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > inviteMaxNameRunes {
		return InviteRecipient{}, inviteReasonName
	}
	return InviteRecipient{Name: name, Email: email}, ""
}

// ParseInviteQty reads the number of tickets a person typed: digits only.
// ok is false for anything else, a zero, or a number past what fits an int.
func ParseInviteQty(s string) (n int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 6 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n > 0
}
