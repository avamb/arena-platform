//go:build integration

// session_change_message_http_integration_test.go — RT-11 (spec 44): the
// optional notice.message_format of PATCH and DELETE .../sessions/{id}. The
// format "art" is stored as filtered ART with the flag "art", the default "text" (the bot,
// every old client) as typed with the flag "text"; an unknown format is 400 before anything is written; a refused text
// is 422 with a reason, and the session does not move.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func (f *scHTTPFixture) journal() (message, format string, rows int) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_changes WHERE session_id = $1`, f.sessionID).Scan(&rows); err != nil {
		f.t.Fatal(err)
	}
	if rows > 0 {
		if err := f.pool.QueryRow(ctx, `SELECT message, message_format FROM session_changes WHERE session_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
			f.sessionID).Scan(&message, &format); err != nil {
			f.t.Fatal(err)
		}
	}
	return message, format, rows
}

func TestSessionChangeHTTP_MessageFormat(t *testing.T) {
	f := newSCHTTPFixture(t)
	base := "/v1/organizations/" + f.orgID.String()
	sessionURL := base + "/events/" + f.eventID.String() + "/sessions/" + f.sessionID.String()
	if st, body := f.do(http.MethodPut, base+"/events/"+f.eventID.String()+"/contact", `{"email":"organizer@example.com"}`); st != http.StatusOK {
		t.Fatalf("set contact: %d %v", st, body)
	}
	move := func(week int, notice string) (int, map[string]any) {
		start := f.start.Add(time.Duration(week) * 7 * 24 * time.Hour)
		return f.do(http.MethodPatch, sessionURL, fmt.Sprintf(`{"start_at":%q,"notice":%s}`, start.Format(time.RFC3339), notice))
	}

	// An unknown format is 400 and nothing is written (the session does not move).
	st, body := move(1, `{"message":"x","message_format":"markdown"}`)
	if st != http.StatusBadRequest || scErrCode(body) != "session.invalid_message_format" {
		t.Fatalf("unknown format: %d %v, want 400 session.invalid_message_format", st, body)
	}
	if !f.sessionStart().Equal(f.start) {
		t.Fatal("the session moved although the format was refused")
	}

	// A refused ART text is 422 with its reason; the session does not move.
	st, body = move(1, `{"message":"<p>a\u202eb</p>","message_format":"art"}`)
	if st != http.StatusUnprocessableEntity || scErrCode(body) != "session.change_message_invalid" {
		t.Fatalf("bidi override: %d %v, want 422 session.change_message_invalid", st, body)
	}
	if details, _ := body["error"].(map[string]any)["details"].(map[string]any); details["reason"] != "control_characters" || details["field"] != "notice.message" {
		t.Fatalf("details = %v", body["error"])
	}
	// 1000 visible characters is the limit, of the TEXT and not of the markup.
	st, body = move(1, fmt.Sprintf(`{"message":%q,"message_format":"art"}`, "<p>"+strings.Repeat("x", 1001)+"</p>"))
	if st != http.StatusUnprocessableEntity || scErrCode(body) != "session.change_message_too_long" {
		t.Fatalf("1001 visible characters: %d %v", st, body)
	}
	if _, _, rows := f.journal(); rows != 0 || !f.sessionStart().Equal(f.start) {
		t.Fatalf("a refused message left a journal row (%d) or moved the session", rows)
	}

	// ART is cut to the whitelist and stored next to the flag.
	st, body = move(1, `{"message":"<div><p onclick=\"x()\">Doors <b>open</b> at <i>19:30</i></p><h3>no</h3><script>y()</script></div>","message_format":"art"}`)
	if st != http.StatusOK {
		t.Fatalf("PATCH art: %d %v", st, body)
	}
	msg, format, rows := f.journal()
	if rows != 1 || format != "art" || msg != "<p>Doors <strong>open</strong> at <em>19:30</em></p><p><strong>no</strong></p>" {
		t.Fatalf("journal after art: rows=%d format=%q message=%q", rows, format, msg)
	}

	// No format at all (the bot, every old client): the plain text is stored as typed, format text.
	st, body = move(2, `{"message":"Moved again.\nSee you <there>."}`)
	if st != http.StatusOK {
		t.Fatalf("PATCH text: %d %v", st, body)
	}
	msg, format, rows = f.journal()
	if rows != 2 || format != "text" || msg != "Moved again.\nSee you <there>." {
		t.Fatalf("journal after text: rows=%d format=%q message=%q", rows, format, msg)
	}

	// The cancellation by DELETE takes the format too, and rejects an unknown one.
	st, body = f.do(http.MethodDelete, sessionURL, `{"notice":{"message":"<p>Cancelled, <strong>sorry</strong>.</p>","message_format":"nope"}}`)
	if st != http.StatusBadRequest || scErrCode(body) != "session.invalid_message_format" {
		t.Fatalf("DELETE unknown format: %d %v", st, body)
	}
	st, body = f.do(http.MethodDelete, sessionURL, `{"notice":{"message":"<p>Cancelled, <strong>sorry</strong>.</p>","message_format":"art"}}`)
	if st != http.StatusOK {
		t.Fatalf("DELETE art: %d %v", st, body)
	}
	msg, format, rows = f.journal()
	if rows != 3 || format != "art" || msg != "<p>Cancelled, <strong>sorry</strong>.</p>" {
		t.Fatalf("journal after delete: rows=%d format=%q message=%q", rows, format, msg)
	}
}
