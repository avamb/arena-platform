//go:build integration

// public_description_integration_test.go - spec 44, RT-03a: the hosted page and
// the public feed return description_html and description_text next to the
// raw description. The raw value is unchanged (an older cached page keeps
// working), the HTML is richtext.Render read with the event's format flag, and
// both the list and the single event of the feed carry the fields.
package hfeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type descFields struct {
	Description     *string `json:"description"`
	DescriptionHTML *string `json:"description_html"`
	DescriptionText *string `json:"description_text"`
}

func (f *publicPageFixture) setDescription(t *testing.T, ctx context.Context, description, format string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx, `UPDATE events SET description = $2, description_format = $3 WHERE id = $1`,
		f.eventID, description, format); err != nil {
		t.Fatalf("set description: %v", err)
	}
}

func (f *publicPageFixture) pageDescription(t *testing.T) descFields {
	t.Helper()
	w := httptest.NewRecorder()
	publicPageHandler(f.pool).HandlePublicPage(w, pageRequest(f.orgSlug, f.eventSlug))
	if w.Code != http.StatusOK {
		t.Fatalf("page status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Event descFields `json:"event"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return body.Event
}

func (f *publicPageFixture) feedEventDescription(t *testing.T) descFields {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.token+"/events/"+f.eventID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("feed_token", f.token)
	rctx.URLParams.Add("event_id", f.eventID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	publicPageHandler(f.pool).HandlePublicFeedEvent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("feed event status = %d (%s)", w.Code, w.Body.String())
	}
	var env struct {
		Event descFields `json:"event"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode feed event: %v", err)
	}
	return env.Event
}

func (f *publicPageFixture) feedListDescription(t *testing.T) descFields {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.token+"/events", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("feed_token", f.token)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	publicPageHandler(f.pool).HandlePublicFeedEvents(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("feed list status = %d (%s)", w.Code, w.Body.String())
	}
	var env struct {
		Events []struct {
			ID string `json:"id"`
			descFields
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode feed list: %v", err)
	}
	for _, e := range env.Events {
		if e.ID == f.eventID.String() {
			return e.descFields
		}
	}
	t.Fatalf("event %s is not in the feed list %s", f.eventID, w.Body.String())
	return descFields{}
}

func wantField(t *testing.T, where, name string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s %s = %v, want %q", where, name, got, want)
	}
}

func TestPublicDescription_ArtRowOnPageAndFeed(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)
	art := `<p>Hello <strong>there</strong> <a href="https://example.com/a">site</a></p><ul><li>one</li><li>two</li></ul>`
	f.setDescription(t, ctx, art, "art")

	wantHTML := `<p>Hello <strong>there</strong> <a href="https://example.com/a" rel="noopener noreferrer" target="_blank">site</a></p><ul><li>one</li><li>two</li></ul>`
	wantText := "Hello there site (https://example.com/a)\n\n• one\n• two"
	for where, d := range map[string]descFields{
		"page":       f.pageDescription(t),
		"feed event": f.feedEventDescription(t),
		"feed list":  f.feedListDescription(t),
	} {
		wantField(t, where, "description (raw, unchanged)", d.Description, art)
		wantField(t, where, "description_html", d.DescriptionHTML, wantHTML)
		wantField(t, where, "description_text", d.DescriptionText, wantText)
	}
}

func TestPublicDescription_TextRowIsConvertedAndNeverGuessed(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)
	// An old text with marks and a literal tag: the flag says text, so the tag
	// is shown as text and the marks become markup.
	old := "**Bold** and <b>literal</b>\n\n- one\n- two"
	f.setDescription(t, ctx, old, "text")

	wantHTML := "<p><strong>Bold</strong> and &lt;b&gt;literal&lt;/b&gt;</p><ul><li>one</li><li>two</li></ul>"
	wantText := "Bold and <b>literal</b>\n\n• one\n• two"
	for where, d := range map[string]descFields{
		"page":       f.pageDescription(t),
		"feed event": f.feedEventDescription(t),
		"feed list":  f.feedListDescription(t),
	} {
		wantField(t, where, "description (raw)", d.Description, old)
		wantField(t, where, "description_html", d.DescriptionHTML, wantHTML)
		wantField(t, where, "description_text", d.DescriptionText, wantText)
	}

	// The same bytes read as art: the flag, not the tags, decides.
	f.setDescription(t, ctx, "<p>2 &lt; 3</p>", "art")
	d := f.pageDescription(t)
	wantField(t, "page", "description_html", d.DescriptionHTML, "<p>2 &lt; 3</p>")
	wantField(t, "page", "description_text", d.DescriptionText, "2 < 3")
}

func TestPublicDescription_HostileArtIsNeverReturnedAsMarkup(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)
	f.setDescription(t, ctx, `<p onclick="x()">a<script>alert(1)</script><a href="javascript:alert(2)">b</a><img src=x onerror=alert(3)></p>`, "art")

	d := f.pageDescription(t)
	if d.DescriptionHTML == nil {
		t.Fatal("description_html is null")
	}
	for _, bad := range []string{"<script", "onclick", "javascript:", "<img", "onerror"} {
		if strings.Contains(*d.DescriptionHTML, bad) {
			t.Errorf("description_html %q contains %q", *d.DescriptionHTML, bad)
		}
	}
}

func TestPublicDescription_NoDescriptionIsNull(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)
	if _, err := pool.Exec(ctx, `UPDATE events SET description = NULL WHERE id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	for where, d := range map[string]descFields{"page": f.pageDescription(t), "feed event": f.feedEventDescription(t)} {
		if d.Description != nil || d.DescriptionHTML != nil || d.DescriptionText != nil {
			t.Errorf("%s: want all three null, got %+v", where, d)
		}
	}
}
