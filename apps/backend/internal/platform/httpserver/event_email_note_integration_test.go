//go:build integration

package httpserver

// event_email_note_integration_test.go — task 7-1 (EC-31): the organizer's own
// letter text of an event. PATCH email_note, the filter, the test-letter route
// and its worker handler, organization isolation, and the event-bundle import
// and read. Everything goes through the REAL router.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
)

type enEnv struct {
	*sec1Env
	events []uuid.UUID
}

func newENEnv(t *testing.T) *enEnv {
	t.Helper()
	e := &enEnv{sec1Env: newSec1Env(t)}
	// Registered after the sec1 cleanup, so it runs BEFORE it: the events
	// have to go before their organizations.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = e.srv.pgxPool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = ANY($2)`,
			eventnote.JobTypeTestEmail, uuidStrings(e.orgs))
		_, _ = e.srv.pgxPool.Exec(ctx, `DELETE FROM events WHERE id = ANY($1)`, e.events)
	})
	return e
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

func (e *enEnv) event(t *testing.T, org uuid.UUID) uuid.UUID {
	t.Helper()
	ev, err := e.q.InsertEvent(context.Background(), org, "Note Event "+uuid.NewString()[:6], nil, "draft", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	e.events = append(e.events, ev.ID)
	return ev.ID
}

func (e *enEnv) noteOf(t *testing.T, ev uuid.UUID) (string, bool) {
	t.Helper()
	var note string
	err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT note FROM event_email_notes WHERE event_id = $1`, ev).Scan(&note)
	if err != nil {
		return "", false
	}
	return note, true
}

func (e *enEnv) emailOf(t *testing.T, user uuid.UUID) string {
	t.Helper()
	var addr string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT email FROM users WHERE id = $1`, user).Scan(&addr); err != nil {
		t.Fatalf("email of user: %v", err)
	}
	return addr
}

func (e *enEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeEvent(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out struct {
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out.Event
}

func errReason(raw string) string {
	var out struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(raw), &out)
	s, _ := out.Error.Details["reason"].(string)
	return s
}

func TestEventEmailNote_PatchSetClearKeepAndFilter(t *testing.T) {
	e := newENEnv(t)
	org := e.org(t, "A")
	owner := e.user(t, "owner")
	e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)
	ev := e.event(t, org)
	path := "/v1/organizations/" + org.String() + "/events/" + ev.String()

	// A text with markup: tags are removed, the paragraphs stay.
	out := e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "",
		`{"email_note":`+jsonString("Come <b>early</b>\r\n\r\n\r\n\r\nSee https://example.com/x")+`}`)
	want := "Come early\n\nSee https://example.com/x"
	if got, _ := decodeEvent(t, out)["email_note"].(string); got != want {
		t.Fatalf("patch answer email_note = %q, want %q", got, want)
	}
	if got, ok := e.noteOf(t, ev); !ok || got != want {
		t.Fatalf("stored = %q (%v), want %q", got, ok, want)
	}
	// The same text through the card read and the org list.
	card := e.expect(t, http.StatusOK, http.MethodGet, "/v1/events/"+ev.String(), tok, "", "")
	if got, _ := decodeEvent(t, card)["email_note"].(string); got != want {
		t.Fatalf("GET event email_note = %q", got)
	}
	list := e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+org.String()+"/events", tok, "", "")
	if !strings.Contains(list, `"email_note":"Come early\n\nSee https://example.com/x"`) {
		t.Fatalf("org list lacks the note: %s", list)
	}

	// A refused text is a 400 with the reason, and NOTHING else of the same
	// request is applied (the name stays).
	for body, reason := range map[string]string{
		`{"name":"Renamed","email_note":` + jsonString("go javascript:alert(1)") + `}`:              "scheme_not_allowed",
		`{"name":"Renamed","email_note":` + jsonString("visit http://example.com") + `}`:            "scheme_not_allowed",
		`{"name":"Renamed","email_note":` + jsonString("bad\u0000byte") + `}`:                       "control_characters",
		`{"name":"Renamed","email_note":` + jsonString(strings.Repeat("x", 2001)) + `}`:             "too_long",
		`{"name":"Renamed","email_note":` + jsonString(strings.Repeat("https://a.test/ ", 6)) + `}`: "too_many_links",
	} {
		raw := e.expect(t, http.StatusBadRequest, http.MethodPatch, path, tok, "", body)
		if sec1Code(raw) != "event.invalid_email_note" || errReason(raw) != reason {
			t.Errorf("%s: code %q reason %q, want event.invalid_email_note / %s", body, sec1Code(raw), errReason(raw), reason)
		}
	}
	if got, _ := e.noteOf(t, ev); got != want {
		t.Fatalf("a refused PATCH changed the note to %q", got)
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE id = $1 AND name LIKE 'Note Event %'`, ev); n != 1 {
		t.Fatal("a refused PATCH renamed the event")
	}

	// Another field alone keeps the note.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"name":"Kept"}`)
	if got, _ := e.noteOf(t, ev); got != want {
		t.Fatalf("a PATCH without email_note changed it to %q", got)
	}
	// "" and null clear.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"email_note":"   "}`)
	if _, ok := e.noteOf(t, ev); ok {
		t.Fatal(`"   " did not clear the note`)
	}
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"email_note":"again"}`)
	out = e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"email_note":null}`)
	if _, ok := e.noteOf(t, ev); ok {
		t.Fatal("null did not clear the note")
	}
	if decodeEvent(t, out)["email_note"] != nil {
		t.Fatalf("cleared note still answered: %s", out)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.event.email_note.update' AND resource_id = $1`, ev.String()); n < 3 {
		t.Errorf("audit rows = %d, want one per change", n)
	}
}

func TestEventEmailNote_StaysInsideTheOrganization(t *testing.T) {
	e := newENEnv(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	ownerA, ownerB := e.user(t, "ownerA"), e.user(t, "ownerB")
	e.member(t, ownerA, orgA, "org_admin")
	e.member(t, ownerB, orgB, "org_admin")
	tokA, tokB := e.token(t, ownerA), e.token(t, ownerB)
	evA := e.event(t, orgA)
	e.expect(t, http.StatusOK, http.MethodPatch, "/v1/organizations/"+orgA.String()+"/events/"+evA.String(), tokA, "", `{"email_note":"secret of A"}`)

	// B's owner, A's event id under B's path: the route's own 404, A's note intact.
	pathB := "/v1/organizations/" + orgB.String() + "/events/" + evA.String()
	e.expect(t, http.StatusNotFound, http.MethodPatch, pathB, tokB, "", `{"email_note":"hijacked"}`)
	e.expect(t, http.StatusNotFound, http.MethodPost, pathB+"/email-note/test", tokB, "", `{}`)
	// B's owner under A's path: not a member there.
	pathA := "/v1/organizations/" + orgA.String() + "/events/" + evA.String()
	e.expect(t, http.StatusForbidden, http.MethodPatch, pathA, tokB, "", `{"email_note":"hijacked"}`)
	e.expect(t, http.StatusForbidden, http.MethodPost, pathA+"/email-note/test", tokB, "", `{}`)
	if got, _ := e.noteOf(t, evA); got != "secret of A" {
		t.Fatalf("A's note = %q", got)
	}
	// And B's note can never be written for A's event by id.
	if n := e.count(t, `SELECT count(*) FROM event_email_notes WHERE org_id = $1`, orgB); n != 0 {
		t.Fatalf("org B holds %d notes", n)
	}
}

// fakeSender records what the worker handler sends.
type fakeSender struct {
	mu   sync.Mutex
	sent []email.Message
}

func (f *fakeSender) Send(_ context.Context, m email.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func TestEventEmailNote_TestLetterRouteAndWorker(t *testing.T) {
	e := newENEnv(t)
	org := e.org(t, "A")
	owner, manager, stranger := e.user(t, "owner"), e.user(t, "manager"), e.user(t, "stranger")
	e.member(t, owner, org, "org_admin")
	e.member(t, manager, org, "organizer")
	tok := e.token(t, owner)
	ev := e.event(t, org)
	test := "/v1/organizations/" + org.String() + "/events/" + ev.String() + "/email-note/test"
	jobsFor := func(to string) int {
		return e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2 AND payload->>'to' = $3`,
			eventnote.JobTypeTestEmail, org.String(), to)
	}

	// Nothing to preview: no stored note and no draft.
	raw := e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, test, tok, "", "")
	if sec1Code(raw) != "event.email_note_empty" {
		t.Errorf("empty: code %q", sec1Code(raw))
	}
	// A refused draft.
	raw = e.expect(t, http.StatusBadRequest, http.MethodPost, test, tok, "", `{"note":"javascript:alert(1)"}`)
	if sec1Code(raw) != "event.invalid_email_note" {
		t.Errorf("bad draft: code %q", sec1Code(raw))
	}

	e.expect(t, http.StatusOK, http.MethodPatch, "/v1/organizations/"+org.String()+"/events/"+ev.String(), tok, "",
		`{"email_note":"Doors close at 19:00.\n\nMap: https://example.com/map"}`)

	// Default recipient: the caller's own address.
	ownerMail := e.emailOf(t, owner)
	out := e.expect(t, http.StatusAccepted, http.MethodPost, test, tok, "", "")
	if !strings.Contains(out, ownerMail) || jobsFor(ownerMail) != 1 {
		t.Fatalf("own address: %s, jobs %d", out, jobsFor(ownerMail))
	}
	// A member of the organization.
	managerMail := e.emailOf(t, manager)
	e.expect(t, http.StatusAccepted, http.MethodPost, test, tok, "", `{"to":`+jsonString(managerMail)+`,"locale":"ru"}`)
	if jobsFor(managerMail) != 1 {
		t.Fatal("no job for the member")
	}
	// A draft that is not stored.
	e.expect(t, http.StatusAccepted, http.MethodPost, test, tok, "", `{"note":"Unsaved draft"}`)

	// Strangers, malformed addresses, and a key with no address of its own.
	raw = e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, test, tok, "", `{"to":`+jsonString(e.emailOf(t, stranger))+`}`)
	if sec1Code(raw) != "event.email_note_test_recipient_not_member" {
		t.Errorf("stranger: code %q", sec1Code(raw))
	}
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, test, tok, "", `{"to":"nobody-`+uuid.NewString()[:6]+`@example.test"}`)
	e.expect(t, http.StatusBadRequest, http.MethodPost, test, tok, "", `{"to":"Name <a@b.test>"}`)
	e.expect(t, http.StatusBadRequest, http.MethodPost, test, tok, "", `{"to":"not an address"}`)
	key := e.key(t, org, "event.update", "event.read")
	raw = e.expect(t, http.StatusBadRequest, http.MethodPost, test, key, "", "")
	if sec1Code(raw) != "event.email_note_test_recipient_required" {
		t.Errorf("key without to: code %q", sec1Code(raw))
	}
	e.expect(t, http.StatusAccepted, http.MethodPost, test, key, "", `{"to":`+jsonString(managerMail)+`}`)

	// The cap: five an hour per organization (four are queued so far).
	e.expect(t, http.StatusAccepted, http.MethodPost, test, tok, "", "")
	raw = e.expect(t, http.StatusTooManyRequests, http.MethodPost, test, tok, "", "")
	if sec1Code(raw) != "event.email_note_test_rate_limited" {
		t.Errorf("sixth: code %q", sec1Code(raw))
	}
	if n := e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.event.email_note.test' AND resource_id = $1`, ev.String()); n != 5 {
		t.Errorf("audit rows = %d, want 5", n)
	}

	// The worker: the stored-note job renders the ticket letter with the block,
	// goes straight to the sender, and records NO email_letters row.
	var payload []byte
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>'to' = $2 AND payload->>'locale' = 'ru'`,
		eventnote.JobTypeTestEmail, managerMail).Scan(&payload); err != nil {
		t.Fatalf("read job: %v", err)
	}
	sender := &fakeSender{}
	h := delivery.NewEventNoteTestHandler(delivery.EventNoteTestOptions{Sender: sender, DB: e.srv.pgxPool})
	if err := h(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d letters", len(sender.sent))
	}
	m := sender.sent[0]
	if m.To != managerMail || !strings.HasPrefix(m.Subject, "[Test] ") {
		t.Errorf("message to %q subject %q", m.To, m.Subject)
	}
	if !strings.Contains(m.TextBody, "Doors close at 19:00.") || !strings.Contains(m.HTMLBody, `href="https://example.com/map"`) ||
		!strings.Contains(m.TextBody, "О мероприятии") {
		t.Errorf("the note block is missing:\n%s", m.TextBody)
	}
	if n := e.count(t, `SELECT count(*) FROM email_letters WHERE org_id = $1`, org); n != 0 {
		t.Errorf("the test letter was recorded as a buyer letter (%d rows)", n)
	}
	// A job whose event is gone, or another organization's, sends nothing.
	foreign := strings.Replace(string(payload), org.String(), uuid.NewString(), 1)
	if err := h(context.Background(), []byte(foreign)); err != nil || len(sender.sent) != 1 {
		t.Errorf("foreign org job: err %v, sent %d", err, len(sender.sent))
	}
}

// The PATCH writes the event row, its metadata and the letter text in ONE
// transaction: when the metadata is refused (a slug another event holds), the
// name and the text of the same request are not applied either.
func TestEventEmailNote_PatchIsAtomicWithTheEventRow(t *testing.T) {
	e := newENEnv(t)
	org := e.org(t, "A")
	owner := e.user(t, "owner")
	e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)
	taker, ev := e.event(t, org), e.event(t, org)
	base := "/v1/organizations/" + org.String() + "/events/"
	slug := "atomic-" + uuid.NewString()[:8]
	e.expect(t, http.StatusOK, http.MethodPatch, base+taker.String(), tok, "", `{"slug":`+jsonString(slug)+`}`)
	e.expect(t, http.StatusOK, http.MethodPatch, base+ev.String(), tok, "", `{"email_note":"first text"}`)

	st, out := e.do(t, http.MethodPatch, base+ev.String(), tok, "",
		`{"name":"Half applied","email_note":"second text","slug":`+jsonString(slug)+`}`)
	if st < 400 {
		t.Fatalf("a duplicate slug was accepted: %d %s", st, out)
	}
	if got, _ := e.noteOf(t, ev); got != "first text" {
		t.Fatalf("the note of a failed PATCH is %q, want the old text", got)
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE id = $1 AND name = 'Half applied'`, ev); n != 0 {
		t.Fatal("the name of a failed PATCH was applied")
	}
	if n := e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.event.email_note.update' AND resource_id = $1`, ev.String()); n != 1 {
		t.Errorf("audit rows = %d: a rolled back change must not be audited", n)
	}
}
