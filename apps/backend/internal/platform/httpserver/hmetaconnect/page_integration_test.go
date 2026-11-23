//go:build integration

package hmetaconnect

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestMetaPage_LiveDB: the owner reads the selected Page live from Graph
// (name, category, link, fan count, picture as a data: URL); nothing of it is
// written anywhere. No connection and no selected page answer 404, a Graph
// error answers 502 without Meta's text and changes nothing, and only a 190
// moves the connection to error.
func TestMetaPage_LiveDB(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if w := e.login(e.ownerA, e.orgA, "code-1"); w.Code != 200 {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	page := func(user, org uuid.UUID) (int, string) {
		w := e.call(http.MethodGet, "/page", "", user, org, e.h.HandlePage)
		return w.Code, w.Body.String()
	}
	snapshot := func() string {
		t.Helper()
		var s string
		if err := e.pool.QueryRow(ctx, `
			SELECT c.status || '|' || COALESCE(c.last_error, '') || '|' || c.updated_at::text || '|' || COALESCE(c.checked_at::text, '') || '|' ||
			       (SELECT count(*) FROM org_meta_assets a WHERE a.org_id = $1)::text || '|' ||
			       (SELECT string_agg(a.kind || ':' || a.meta_id || ':' || a.name || ':' || a.selected::text, ',' ORDER BY a.kind, a.meta_id)
			          FROM org_meta_assets a WHERE a.org_id = $1) || '|' ||
			       (SELECT count(*) FROM audit_events WHERE resource_id = $2)::text
			  FROM org_meta_connections c WHERE c.org_id = $1`, e.orgA, e.orgA.String()).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The single shared page was selected at connect.
	before := snapshot()
	code, body := page(e.ownerA, e.orgA)
	if code != 200 {
		t.Fatalf("page: %d %s", code, body)
	}
	var got struct {
		MetaID   string  `json:"meta_id"`
		Name     string  `json:"name"`
		Category *string `json:"category"`
		Link     *string `json:"link"`
		FanCount *int64  `json:"fan_count"`
		Picture  *string `json:"picture_data_url"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.MetaID != "3001" || got.Name != "Club page live" || got.Category == nil || *got.Category != "Concert venue" ||
		got.Link == nil || *got.Link != "https://www.facebook.com/club" || got.FanCount == nil || *got.FanCount != 4321 ||
		got.Picture == nil || *got.Picture != "data:image/png;base64,UE5HREFUQQ==" || strings.Contains(body, itToken) {
		t.Fatalf("page answer: %s", body)
	}
	if after := snapshot(); after != before {
		t.Fatalf("the page preview wrote to the database:\n%s\n%s", before, after)
	}
	var stored int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND (metadata::text LIKE '%Club page live%' OR metadata::text LIKE '%4321%')`,
		e.orgA.String()).Scan(&stored)
	if stored != 0 {
		t.Fatal("the page preview reached the audit")
	}

	// Another organization without a connection.
	if code, body := page(e.ownerB, e.orgB); code != 404 || !strings.Contains(body, "meta.not_connected") {
		t.Fatalf("no connection: %d %s", code, body)
	}

	// PATCH page_id: a page of this connection, then an unknown one.
	if w := e.call(http.MethodPatch, "/", `{"page_id":"3001"}`, e.ownerA, e.orgA, e.h.HandlePatch); w.Code != 200 {
		t.Fatalf("patch page: %d %s", w.Code, w.Body.String())
	}
	if w := e.call(http.MethodPatch, "/", `{"page_id":"9999"}`, e.ownerA, e.orgA, e.h.HandlePatch); w.Code != 422 || !strings.Contains(w.Body.String(), "meta.asset_not_found") {
		t.Fatalf("patch unknown page: %d %s", w.Code, w.Body.String())
	}
	var selects int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action = 'v1.org.meta.select' AND metadata->>'page_id' = '3001'`, e.orgA.String()).Scan(&selects)
	if selects != 1 {
		t.Fatalf("select audit with page_id = %d", selects)
	}

	// No page selected.
	if _, err := e.pool.Exec(ctx, `UPDATE org_meta_assets SET selected = false WHERE org_id = $1 AND kind = 'page'`, e.orgA); err != nil {
		t.Fatal(err)
	}
	reads := e.graph.pageReads
	if code, body := page(e.ownerA, e.orgA); code != 404 || !strings.Contains(body, "meta.no_page") || e.graph.pageReads != reads {
		t.Fatalf("no page: %d %s reads=%d", code, body, e.graph.pageReads-reads)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE org_meta_assets SET selected = true WHERE org_id = $1 AND kind = 'page' AND meta_id = '3001'`, e.orgA); err != nil {
		t.Fatal(err)
	}

	// A Graph error that is not 190: 502, Meta's text is not passed on, nothing written.
	before = snapshot()
	e.graph.mu.Lock()
	e.graph.pageFail = 10
	e.graph.mu.Unlock()
	if code, body := page(e.ownerA, e.orgA); code != 502 || !strings.Contains(body, "meta.graph_error") || strings.Contains(body, "Meta secret words") {
		t.Fatalf("graph error: %d %s", code, body)
	}
	if after := snapshot(); after != before {
		t.Fatalf("a failed preview wrote to the database:\n%s\n%s", before, after)
	}

	// 190: the connection moves to error and the token is wiped.
	e.graph.mu.Lock()
	e.graph.pageFail = 190
	e.graph.mu.Unlock()
	if code, body := page(e.ownerA, e.orgA); code != 502 || !strings.Contains(body, "meta.graph_error") {
		t.Fatalf("190: %d %s", code, body)
	}
	var status string
	var hasToken bool
	if err := e.pool.QueryRow(ctx, `SELECT status, access_token_enc IS NOT NULL FROM org_meta_connections WHERE org_id = $1`, e.orgA).Scan(&status, &hasToken); err != nil ||
		status != "error" || hasToken {
		t.Fatalf("after 190: %s token=%v %v", status, hasToken, err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE payload->>'user_id' = ANY($1)`, []string{e.ownerA.String(), e.mgrA.String()})
	})
	var notices int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE payload->>'user_id' = $1 AND payload->>'kind' = 'meta_connection_broken'`, e.ownerA.String()).Scan(&notices)
	if notices != 1 {
		t.Fatalf("owner notices after 190 = %d", notices)
	}
	if code, body := page(e.ownerA, e.orgA); code != 409 || !strings.Contains(body, "meta.not_active") {
		t.Fatalf("error connection: %d %s", code, body)
	}
}
