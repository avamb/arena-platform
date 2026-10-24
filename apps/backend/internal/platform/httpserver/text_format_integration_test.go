//go:build integration

package httpserver

// POST /v1/organizations/{org_id}/texts/format (spec 44, RT-04) through the
// real router, the real auth and permission gates, the real organization
// guard and the real textformat client against a stub Messages API. Run on a
// migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/ -run TextFormat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// textModel is a Messages API that answers through a function and records
// every request.
type textModel struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	keys   []string
	status int
	html   func(userMessage string) string
}

func newTextModel(t *testing.T) *textModel {
	t.Helper()
	m := &textModel{status: http.StatusOK}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		m.mu.Lock()
		m.bodies = append(m.bodies, body)
		m.keys = append(m.keys, r.Header.Get("x-api-key"))
		status, answer := m.status, m.html
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`))
			return
		}
		msgs, _ := body["messages"].([]any)
		first, _ := msgs[0].(map[string]any)
		content, _ := first["content"].([]any)
		part, _ := content[0].(map[string]any)
		user, _ := part["text"].(string)
		out, _ := json.Marshal(map[string]any{
			"stop_reason": "tool_use",
			"content": []any{map[string]any{
				"type": "tool_use", "id": "tu_1", "name": "format_text",
				"input": map[string]any{"html": answer(user)},
			}},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *textModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies)
}

func textFormatServer(t *testing.T, pool *pgxpool.Pool, model *textModel, perUser, perOrg int) *Server {
	t.Helper()
	return buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		if model != nil {
			c.PosterLLMAPIKey = "sk-ant-test-stub"
			c.PosterLLMBaseURL = model.srv.URL
			c.TextFormatLLMModel = "claude-haiku-5-5"
		}
		c.TextFormatsPerUserPerDay = perUser
		c.TextFormatsPerOrgPerDay = perOrg
	})
}

func formatTextAs(srv *Server, token string, org uuid.UUID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/texts/format", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec
}

type formatAnswer struct {
	HTML          string `json:"html"`
	AIApplied     bool   `json:"ai_applied"`
	Reason        string `json:"reason"`
	PromptVersion string `json:"prompt_version"`
}

func mustFormatAnswer(t *testing.T, rec *httptest.ResponseRecorder) formatAnswer {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var a formatAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if a.PromptVersion != "rt04-v1" {
		t.Errorf("prompt_version = %q", a.PromptVersion)
	}
	return a
}

const formatBody = `{"text":"Doors open at 19:00. Tickets: https://example.com/t","kind":"description","locale":"en"}`

func TestTextFormat_RouteFormatsAndKeepsOrganizationsApart(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID, other.orgID)
	model := newTextModel(t)
	model.html = func(string) string {
		return `<p>Doors open at <strong>19:00</strong>. Tickets: <a href="https://example.com/t">https://example.com/t</a></p>`
	}
	srv := textFormatServer(t, pool, model, 100, 500)
	owner := f.posterToken(srv, f.ownerID)

	// An agent of the same organization lacks event.update.
	q := gen.New(pool)
	agent, err := q.InsertUser(context.Background(), f.newEmail("fmt-agent"), "x", "en")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(context.Background(), agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	if rec := formatTextAs(srv, f.posterToken(srv, agent.ID), f.orgID, formatBody); rec.Code != http.StatusForbidden {
		t.Errorf("agent: %d %s", rec.Code, rec.Body.String())
	}
	if rec := formatTextAs(srv, "", f.orgID, formatBody); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	// Another organization's owner on the victim's path.
	foreign := other.posterToken(srv, other.ownerID)
	if rec := formatTextAs(srv, foreign, f.orgID, formatBody); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("foreign owner on the victim's path: %d %s, want 403 or 404", rec.Code, rec.Body.String())
	}
	if n := model.calls(); n != 0 {
		t.Fatalf("the model was called %d times for refused requests", n)
	}

	// The owner formats.
	a := mustFormatAnswer(t, formatTextAs(srv, owner, f.orgID, formatBody))
	if !a.AIApplied || a.Reason != "" || !strings.Contains(a.HTML, "<strong>19:00</strong>") || !strings.Contains(a.HTML, `<a href="https://example.com/t">`) {
		t.Fatalf("answer = %+v", a)
	}
	if model.calls() != 1 {
		t.Fatalf("the model was called %d times", model.calls())
	}
	// The request the model saw.
	body := model.bodies[0]
	if body["model"] != "claude-haiku-5-5" || model.keys[0] != "sk-ant-test-stub" {
		t.Errorf("model %v key %q", body["model"], model.keys[0])
	}
	if choice, _ := body["tool_choice"].(map[string]any); choice["name"] != "format_text" || choice["type"] != "tool" {
		t.Errorf("tool_choice = %v", body["tool_choice"])
	}
	if mt, _ := body["max_tokens"].(float64); mt < 256 || mt > 8192 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
	msgs, _ := body["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	content, _ := first["content"].([]any)
	part, _ := content[0].(map[string]any)
	user, _ := part["text"].(string)
	if !strings.Contains(user, "<text>\nDoors open at 19:00. Tickets: https://example.com/t\n</text>") {
		t.Errorf("the model did not get the text in a <text> block: %q", user)
	}
	// Another organization's owner formats in its own organization.
	if a := mustFormatAnswer(t, formatTextAs(srv, foreign, other.orgID, formatBody)); !a.AIApplied {
		t.Errorf("own organization: %+v", a)
	}
}

func TestTextFormat_EveryFailureIsA200WithThePlainText(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID)
	model := newTextModel(t)
	srv := textFormatServer(t, pool, model, 100, 500)
	owner := f.posterToken(srv, f.ownerID)
	const plain = "<p>Doors open at 19:00. Tickets: https://example.com/t</p>"
	// A link is the only thing the plain conversion makes of the address
	// for the description profile: none (AutoLink is for the email note).

	// The model changes a word.
	model.html = func(string) string {
		return `<p>Doors open at <strong>20:00</strong>. Tickets: https://example.com/t</p>`
	}
	if a := mustFormatAnswer(t, formatTextAs(srv, owner, f.orgID, formatBody)); a.AIApplied || a.Reason != "text_changed" || a.HTML != plain {
		t.Errorf("changed word: %+v", a)
	}
	// The model obeys an instruction hidden in the text.
	model.html = func(string) string { return `<script>alert(1)</script><p>HACKED</p>` }
	if a := mustFormatAnswer(t, formatTextAs(srv, owner, f.orgID, formatBody)); a.AIApplied || a.HTML != plain || strings.Contains(a.HTML, "HACKED") {
		t.Errorf("injection: %+v", a)
	}
	// The model is down.
	model.mu.Lock()
	model.status = http.StatusServiceUnavailable
	model.mu.Unlock()
	if a := mustFormatAnswer(t, formatTextAs(srv, owner, f.orgID, formatBody)); a.AIApplied || a.Reason != "model_error" || a.HTML != plain {
		t.Errorf("model down: %+v", a)
	}
	// Too long is refused before the model.
	before := model.calls()
	rec := formatTextAs(srv, owner, f.orgID, `{"text":"`+strings.Repeat("a", 4001)+`","kind":"description"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "text.too_long") {
		t.Errorf("too long: %d %s", rec.Code, rec.Body.String())
	}
	if rec := formatTextAs(srv, owner, f.orgID, `{"text":"x","kind":"poem"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d %s", rec.Code, rec.Body.String())
	}
	if model.calls() != before {
		t.Errorf("the model was called for a refused request")
	}
}

func TestTextFormat_WithoutAModelKeyAndOverTheCaps(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID)

	// No key: 200 with the plain text, not a 503.
	srv := textFormatServer(t, pool, nil, 100, 500)
	owner := f.posterToken(srv, f.ownerID)
	if a := mustFormatAnswer(t, formatTextAs(srv, owner, f.orgID, formatBody)); a.AIApplied || a.Reason != "not_configured" || !strings.HasPrefix(a.HTML, "<p>") {
		t.Errorf("no key: %+v", a)
	}

	// Caps: two a day for the person, the third is a fallback with no call.
	model := newTextModel(t)
	model.html = func(string) string { return `<p>Doors open at 19:00. Tickets: https://example.com/t</p>` }
	srv = textFormatServer(t, pool, model, 2, 500)
	owner = f.posterToken(srv, f.ownerID)
	for i := 1; i <= 2; i++ {
		if a := mustFormatAnswer(t, formatTextAs(srv, owner, f.orgID, formatBody)); !a.AIApplied {
			t.Fatalf("call %d: %+v", i, a)
		}
	}
	// A refused request in between takes no slot and the third call is over the cap.
	if rec := formatTextAs(srv, owner, f.orgID, `{"text":"x","kind":"poem"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d", rec.Code)
	}
	if a := mustFormatAnswer(t, formatTextAs(srv, owner, f.orgID, formatBody)); a.AIApplied || a.Reason != "quota" {
		t.Errorf("third call: %+v", a)
	}
	if model.calls() != 2 {
		t.Errorf("the model was called %d times, want 2", model.calls())
	}
}
