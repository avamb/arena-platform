package htextformat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/textformat"
)

// fakeFormatter returns a canned answer and counts the calls.
type fakeFormatter struct {
	calls int
	got   textformat.Input
	html  string
	err   error
	block bool
	// usage is reported with every answer; boom makes the call panic.
	usage textformat.Usage
	boom  bool
}

func (f *fakeFormatter) Format(ctx context.Context, in textformat.Input) (textformat.Output, error) {
	f.calls++
	f.got = in
	if f.boom {
		panic("the formatter blew up")
	}
	if f.block {
		<-ctx.Done()
		return textformat.Output{}, ctx.Err()
	}
	return textformat.Output{HTML: f.html, Usage: f.usage}, f.err
}

type rig struct {
	orgID uuid.UUID
	f     *fakeFormatter
	h     *Handler
	logs  *bytes.Buffer
	allow bool
	user  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	rg := &rig{orgID: uuid.New(), f: &fakeFormatter{}, logs: &bytes.Buffer{}, allow: true, user: uuid.NewString()}
	rg.h = New(slog.New(slog.NewTextHandler(rg.logs, nil))).
		WithFormatter(rg.f).
		WithRowOrgAccess(func(w http.ResponseWriter, _ *http.Request, _ uuid.UUID, write bool, code, _ string) bool {
			if !write {
				t.Errorf("formatting must be a write for the organization guard")
			}
			if !rg.allow {
				http.Error(w, `{"error":{"code":"`+code+`"}}`, http.StatusNotFound)
				return false
			}
			return true
		})
	return rg
}

func (rg *rig) post(org, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/organizations/{org_id}/texts/format", rg.h.Format)
	req := httptest.NewRequest(http.MethodPost, "/organizations/"+org+"/texts/format", strings.NewReader(body))
	req = req.WithContext(auth.WithActor(req.Context(), auth.Actor{ID: rg.user, Type: auth.ActorTypeUser}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (rg *rig) format(kind, text string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"text": text, "kind": kind, "locale": "en"})
	return rg.post(rg.orgID.String(), string(b))
}

type answer struct {
	HTML          string `json:"html"`
	AIApplied     bool   `json:"ai_applied"`
	Reason        string `json:"reason"`
	PromptVersion string `json:"prompt_version"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) answer {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var a answer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if a.PromptVersion != textformat.PromptVersion {
		t.Errorf("prompt_version = %q", a.PromptVersion)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	return a
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not an error envelope: %q", rec.Body.String())
	}
	return env.Error.Code
}

const plainFallback = "<p>Hello world</p>"

func TestFormat_GoodAnswerIsApplied(t *testing.T) {
	rg := newRig(t)
	rg.f.html = "<p>Hello <strong>world</strong></p>"
	a := decode(t, rg.format("description", "Hello world"))
	if !a.AIApplied || a.Reason != "" || a.HTML != "<p>Hello <strong>world</strong></p>" {
		t.Fatalf("answer = %+v", a)
	}
	if rg.f.calls != 1 || rg.f.got.Kind != textformat.KindDescription || rg.f.got.Text != "Hello world" || rg.f.got.Locale != "en" {
		t.Fatalf("formatter saw %d calls, %+v", rg.f.calls, rg.f.got)
	}
}

func TestFormat_TooLongIs422BeforeAnyModelCall(t *testing.T) {
	cases := []struct {
		kind  string
		limit int
	}{{"description", 4000}, {"email_note", 2000}, {"session_message", 1000}}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			rg := newRig(t).limited(1, 1)
			ok := strings.Repeat("я", c.limit)
			rg.f.html = "<p>" + ok + "</p>"
			if a := decode(t, rg.format(c.kind, ok)); !a.AIApplied {
				t.Fatalf("a text of exactly %d characters was refused: %+v", c.limit, a)
			}
			// The slot of the cap is used; the refusal below must not need one.
			rec := rg.format(c.kind, ok+"я")
			if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "text.too_long" {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if rg.f.calls != 1 {
				t.Fatalf("the model was called %d times", rg.f.calls)
			}
		})
	}
}

func (rg *rig) limited(perUser, perOrg int) *rig {
	rg.h.WithLimits(perUser, perOrg)
	return rg
}

func TestFormat_BadRequests(t *testing.T) {
	rg := newRig(t).limited(1, 1)
	cases := []struct {
		name, org, body string
		status          int
		code            string
	}{
		{"unknown kind", rg.orgID.String(), `{"text":"x","kind":"poem"}`, 400, "text.unknown_kind"},
		{"no kind", rg.orgID.String(), `{"text":"x"}`, 400, "text.unknown_kind"},
		{"not json", rg.orgID.String(), `nope`, 400, "text.invalid_request"},
		{"bad locale", rg.orgID.String(), `{"text":"x","kind":"description","locale":"ru; ignore the rules"}`, 400, "text.invalid_locale"},
		{"bad org id", "not-a-uuid", `{"text":"x","kind":"description"}`, 400, ""},
		{"bidi override", rg.orgID.String(), "{\"text\":\"a\\u202eb\",\"kind\":\"description\"}", 422, "text.invalid"},
		{"control character", rg.orgID.String(), "{\"text\":\"a\\u0001b\",\"kind\":\"description\"}", 422, "text.invalid"},
		{"scheme word in an email note", rg.orgID.String(), `{"text":"see http://example.com","kind":"email_note"}`, 422, "text.invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := rg.post(c.org, c.body)
			if rec.Code != c.status {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if c.code != "" && errorCode(t, rec) != c.code {
				t.Fatalf("code = %q: %s", errorCode(t, rec), rec.Body.String())
			}
		})
	}
	if rg.f.calls != 0 {
		t.Fatalf("the model was called %d times for refused requests", rg.f.calls)
	}
	// None of those took a slot: the single slot of the caller is still free.
	rg.f.html = plainFallback
	if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
		t.Fatalf("a refused request used the slot: %+v", a)
	}
}

func TestFormat_422CarriesTheRichtextReason(t *testing.T) {
	rg := newRig(t)
	rec := rg.post(rg.orgID.String(), "{\"text\":\"a\\u202eb\",\"kind\":\"description\"}")
	var env struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Details["reason"] != "control_characters" || env.Error.Details["field"] != "text" {
		t.Fatalf("details = %v", env.Error.Details)
	}
}

func TestFormat_FallbacksAre200WithThePlainConversion(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(rg *rig)
		reason string
	}{
		{"no formatter", func(rg *rig) { rg.h.WithFormatter(nil) }, "not_configured"},
		{"formatter says not configured", func(rg *rig) { rg.f.err = textformat.ErrNotConfigured }, "not_configured"},
		{"model error", func(rg *rig) { rg.f.err = &textformat.APIError{Status: 529, Type: "overloaded_error"} }, "model_error"},
		{"model answer cut", func(rg *rig) { rg.f.err = textformat.ErrTruncated }, "model_error"},
		{"no tool call", func(rg *rig) { rg.f.err = textformat.ErrNoToolCall }, "model_error"},
		{"model did not finish", func(rg *rig) { rg.f.err = textformat.ErrIncomplete }, "model_error"},
		{"answer too large", func(rg *rig) { rg.f.err = textformat.ErrAnswerTooLarge }, "model_error"},
		{"the formatter panics", func(rg *rig) { rg.f.boom = true }, "model_error"},
		{"unknown failure", func(rg *rig) { rg.f.err = errors.New("boom: the organizer's secret text") }, "model_error"},
		{"empty answer", func(rg *rig) { rg.f.html = "" }, "empty_output"},
		{"changed word", func(rg *rig) { rg.f.html = "<p>Hello <strong>word</strong></p>" }, "text_changed"},
		{"added text", func(rg *rig) { rg.f.html = "<p>Hello world</p><p>BUY NOW</p>" }, "text_changed"},
		{"injection", func(rg *rig) { rg.f.html = "<script>alert(1)</script>" }, "empty_output"},
		{"forbidden characters in the answer", func(rg *rig) { rg.f.html = "<p>Hello wor\u202eld</p>" }, "invalid_markup"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rg := newRig(t)
			c.setup(rg)
			a := decode(t, rg.format("description", "Hello world"))
			if a.AIApplied || a.Reason != c.reason || a.HTML != plainFallback {
				t.Fatalf("answer = %+v, want reason %q and the plain conversion", a, c.reason)
			}
		})
	}
}

func TestFormat_Timeout(t *testing.T) {
	rg := newRig(t)
	rg.h.timeout = 40 * time.Millisecond
	rg.f.block = true
	start := time.Now()
	a := decode(t, rg.format("description", "Hello world"))
	if a.AIApplied || a.Reason != "timeout" || a.HTML != plainFallback {
		t.Fatalf("answer = %+v", a)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the call was not cut by its own deadline")
	}
}

func TestFormat_DefaultDeadlineIs25Seconds(t *testing.T) {
	if got := New(slog.Default()).timeout; got != 25*time.Second {
		t.Fatalf("timeout = %v", got)
	}
}

func TestFormat_BlankTextIsAFallbackWithoutACall(t *testing.T) {
	rg := newRig(t).limited(1, 1)
	a := decode(t, rg.format("description", "  \n "))
	if a.AIApplied || a.Reason != "no_text" || a.HTML != "" || rg.f.calls != 0 {
		t.Fatalf("answer = %+v, calls = %d", a, rg.f.calls)
	}
}

func TestFormat_QuotaPerUserAndPerOrganization(t *testing.T) {
	rg := newRig(t).limited(2, 3)
	rg.f.html = plainFallback
	for i := 0; i < 2; i++ {
		if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
			t.Fatalf("call %d: %+v", i+1, a)
		}
	}
	// The third call of the same person is over the personal cap: no call.
	a := decode(t, rg.format("description", "Hello world"))
	if a.AIApplied || a.Reason != "quota" || a.HTML != plainFallback {
		t.Fatalf("over the personal cap: %+v", a)
	}
	if rg.f.calls != 2 {
		t.Fatalf("the model was called %d times", rg.f.calls)
	}
	// Another person of the same organization has the organization's last slot.
	rg.user = uuid.NewString()
	if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
		t.Fatalf("another person: %+v", a)
	}
	// Now the organization is out, whoever asks.
	rg.user = uuid.NewString()
	if a := decode(t, rg.format("description", "Hello world")); a.AIApplied || a.Reason != "quota" {
		t.Fatalf("over the organization cap: %+v", a)
	}
	if rg.f.calls != 3 {
		t.Fatalf("the model was called %d times", rg.f.calls)
	}
	// Another organization has its own counter.
	other := rg.orgID
	rg.orgID = uuid.New()
	if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
		t.Fatalf("another organization: %+v", a)
	}
	rg.orgID = other
}

func TestFormat_AFailedModelCallKeepsItsSlot(t *testing.T) {
	rg := newRig(t).limited(1, 5)
	rg.f.err = &textformat.APIError{Status: 500}
	if a := decode(t, rg.format("description", "Hello world")); a.Reason != "model_error" {
		t.Fatalf("answer = %+v", a)
	}
	rg.f.err = nil
	rg.f.html = plainFallback
	if a := decode(t, rg.format("description", "Hello world")); a.Reason != "quota" {
		t.Fatalf("the call may have been billed, the slot stays used: %+v", a)
	}
}

func TestFormat_NoCapWhenLimitsAreZero(t *testing.T) {
	rg := newRig(t).limited(0, 0)
	rg.f.html = plainFallback
	for i := 0; i < 5; i++ {
		if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
			t.Fatalf("call %d: %+v", i+1, a)
		}
	}
}

func TestFormat_QuotaIsNotTakenWithoutAFormatter(t *testing.T) {
	rg := newRig(t).limited(1, 1)
	rg.h.WithFormatter(nil)
	for i := 0; i < 3; i++ {
		if a := decode(t, rg.format("description", "Hello world")); a.Reason != "not_configured" {
			t.Fatalf("call %d: %+v", i+1, a)
		}
	}
	rg.h.WithFormatter(rg.f)
	rg.f.html = plainFallback
	if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
		t.Fatalf("not-configured requests used the slot: %+v", a)
	}
}

func TestFormat_AnotherOrganizationIsA404(t *testing.T) {
	rg := newRig(t).limited(1, 1)
	rg.allow = false
	rec := rg.format("description", "Hello world")
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "org.not_found" {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rg.f.calls != 0 {
		t.Fatal("the model was called for a foreign organization")
	}
	// The guard comes before the body is even read, so a foreign caller
	// learns nothing from the validation errors either.
	if rec := rg.post(rg.orgID.String(), `nope`); rec.Code != http.StatusNotFound {
		t.Fatalf("a foreign caller saw a validation error: %d", rec.Code)
	}
	rg.allow = true
	rg.f.html = plainFallback
	if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
		t.Fatalf("a refused request used the slot: %+v", a)
	}
}

func TestFormat_WithoutAGuardIs503(t *testing.T) {
	rg := newRig(t)
	rg.h.WithRowOrgAccess(nil)
	rec := rg.format("description", "Hello world")
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "dependency.org_access_unavailable" {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

// The text and the model's answer are never logged, in any outcome.
func TestFormat_LogsHoldNoText(t *testing.T) {
	secretText := "ORGANIZER-SECRET-TEXT-4711"
	secretAnswer := "MODEL-SECRET-ANSWER-0815"
	cases := []func(rg *rig){
		func(rg *rig) { rg.f.html = "<p>" + secretText + "</p>" },
		func(rg *rig) { rg.f.html = "<p>" + secretAnswer + "</p>" },
		func(rg *rig) { rg.f.err = errors.New(secretText + " " + secretAnswer) },
		func(rg *rig) { rg.f.err = &textformat.APIError{Status: 400, Type: "invalid_request_error"} },
	}
	for i, setup := range cases {
		rg := newRig(t).limited(100, 500)
		setup(rg)
		decode(t, rg.format("description", secretText))
		logs := rg.logs.String()
		if strings.Contains(logs, secretText) || strings.Contains(logs, secretAnswer) {
			t.Fatalf("case %d: the log holds a text: %s", i, logs)
		}
		for _, want := range []string{"org_id=" + rg.orgID.String(), "kind=description", "prompt_version=" + textformat.PromptVersion, "text_chars=" + "26"} {
			if !strings.Contains(logs, want) {
				t.Errorf("case %d: the log lacks %q: %s", i, want, logs)
			}
		}
	}
	// A refusal logs nothing about the text either.
	rg := newRig(t)
	rg.post(rg.orgID.String(), "{\"text\":\""+secretText+"\\u202e\",\"kind\":\"description\"}")
	if strings.Contains(rg.logs.String(), secretText) {
		t.Fatalf("the refusal logged the text: %s", rg.logs.String())
	}
}

// The cap of a person is the person's id: a call with no actor is refused, it
// never shares a bucket with other such calls.
func TestFormat_NoActorIsRefused(t *testing.T) {
	rg := newRig(t).limited(5, 5)
	r := chi.NewRouter()
	r.Post("/organizations/{org_id}/texts/format", rg.h.Format)
	req := httptest.NewRequest(http.MethodPost, "/organizations/"+rg.orgID.String()+"/texts/format",
		strings.NewReader(`{"text":"Hello world","kind":"description"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "auth.unauthorized" {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rg.f.calls != 0 {
		t.Fatal("the model was called without a person")
	}
}

// A panic in the formatter or in the verification is a 200 fallback and the
// log holds no text.
func TestFormat_PanicIsAFallbackWithoutTheText(t *testing.T) {
	rg := newRig(t).limited(5, 5)
	rg.f.boom = true
	a := decode(t, rg.format("description", "ORGANIZER-SECRET-TEXT-4711"))
	if a.AIApplied || a.Reason != "model_error" || !strings.Contains(a.HTML, "ORGANIZER-SECRET-TEXT-4711") {
		t.Fatalf("answer = %+v", a)
	}
	logs := rg.logs.String()
	if !strings.Contains(logs, "panicked") || !strings.Contains(logs, "model_error=panic") {
		t.Fatalf("the panic is not in the log: %s", logs)
	}
	if strings.Contains(logs, "ORGANIZER-SECRET-TEXT-4711") {
		t.Fatalf("the log holds the text: %s", logs)
	}
}

// The limit is on the visible text: markers of the old notation do not count,
// and only a text of more than four times the limit is refused before parsing.
func TestFormat_LimitCountsVisibleCharacters(t *testing.T) {
	rg := newRig(t).limited(10, 10)
	visible := strings.Repeat("я", 4000)
	rg.f.html = "<p><strong>" + visible + "</strong></p>"
	if a := decode(t, rg.format("description", "**"+visible+"**")); !a.AIApplied {
		t.Fatalf("4000 visible characters in bold were refused: %+v", a)
	}
	// 4001 visible characters under markers: the limit of the visible text.
	rec := rg.format("description", "**"+visible+"я**")
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "text.too_long" {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	// More than four times the limit never reaches the parser.
	rec = rg.format("description", strings.Repeat("я", 4*4000+1))
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "text.too_long" {
		t.Fatalf("status = %d", rec.Code)
	}
	if rg.f.calls != 1 {
		t.Fatalf("the model was called %d times", rg.f.calls)
	}
}

func TestFormat_UsageIsLoggedAsNumbers(t *testing.T) {
	rg := newRig(t).limited(10, 10)
	rg.f.html = plainFallback
	rg.f.usage = textformat.Usage{InputTokens: 812, OutputTokens: 1450}
	decode(t, rg.format("description", "Hello world"))
	logs := rg.logs.String()
	if !strings.Contains(logs, "input_tokens=812") || !strings.Contains(logs, "output_tokens=1450") {
		t.Fatalf("usage is not in the log: %s", logs)
	}
	// A truncated answer costs tokens too.
	rg.f.err = textformat.ErrTruncated
	rg.logs.Reset()
	decode(t, rg.format("description", "Hello world"))
	if !strings.Contains(rg.logs.String(), "output_tokens=1450") || !strings.Contains(rg.logs.String(), "answer_truncated") {
		t.Fatalf("log: %s", rg.logs.String())
	}
}

func TestFormat_LostEmailNoteLinksFallBack(t *testing.T) {
	rg := newRig(t)
	rg.f.html = "<p>Tickets: https://x.example.com/a</p>"
	a := decode(t, rg.format("email_note", "Tickets: https://x.example.com/a"))
	if a.AIApplied || a.Reason != "links_lost" {
		t.Fatalf("answer = %+v", a)
	}
}

// A person whose own cap is full does not burn a slot of the organization.
func TestFormat_RefusedPersonKeepsTheOrganizationSlot(t *testing.T) {
	rg := newRig(t).limited(1, 2)
	rg.f.html = plainFallback
	if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
		t.Fatalf("first call: %+v", a)
	}
	// The same person is over the cap, many times: the organization still has a slot.
	for i := 0; i < 5; i++ {
		if a := decode(t, rg.format("description", "Hello world")); a.Reason != "quota" {
			t.Fatalf("over the personal cap: %+v", a)
		}
	}
	rg.user = uuid.NewString()
	if a := decode(t, rg.format("description", "Hello world")); !a.AIApplied {
		t.Fatalf("the organization's slot was burnt by refused calls: %+v", a)
	}
}

// safeFormatter is a formatter that can be called from many goroutines.
type safeFormatter struct{ calls atomic.Int32 }

func (f *safeFormatter) Format(context.Context, textformat.Input) (textformat.Output, error) {
	f.calls.Add(1)
	return textformat.Output{HTML: plainFallback}, nil
}

// Two requests of two people race for the last slot of the organization: one
// gets the call, the other is told "quota", and the loser keeps its own slot.
func TestFormat_RaceForTheLastOrganizationSlot(t *testing.T) {
	for round := 0; round < 50; round++ {
		orgID := uuid.New()
		f := &safeFormatter{}
		h := New(slog.New(slog.NewTextHandler(io.Discard, nil))).
			WithFormatter(f).
			WithLimits(1, 1).
			WithRowOrgAccess(func(http.ResponseWriter, *http.Request, uuid.UUID, bool, string, string) bool { return true })
		r := chi.NewRouter()
		r.Post("/organizations/{org_id}/texts/format", h.Format)
		users := []string{uuid.NewString(), uuid.NewString()}
		codes := make([]string, len(users))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, u := range users {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, "/organizations/"+orgID.String()+"/texts/format",
					strings.NewReader(`{"text":"Hello world","kind":"description"}`))
				req = req.WithContext(auth.WithActor(req.Context(), auth.Actor{ID: u, Type: auth.ActorTypeUser}))
				rec := httptest.NewRecorder()
				<-start
				r.ServeHTTP(rec, req)
				var a answer
				_ = json.Unmarshal(rec.Body.Bytes(), &a)
				codes[i] = a.Reason
				if a.AIApplied {
					codes[i] = "applied"
				}
			}()
		}
		close(start)
		wg.Wait()
		applied, quota := 0, 0
		for _, c := range codes {
			switch c {
			case "applied":
				applied++
			case "quota":
				quota++
			}
		}
		if applied != 1 || quota != 1 || f.calls.Load() != 1 {
			t.Fatalf("round %d: codes %v, calls %d", round, codes, f.calls.Load())
		}
		// Whoever lost still has a personal slot: with a fresh organization
		// counter (a new handler cannot be used) it is visible through Count.
		for i, c := range codes {
			if c == "quota" && h.userQuota.Count(users[i]) != 0 {
				t.Fatalf("round %d: the loser's personal slot was burnt", round)
			}
		}
	}
}
