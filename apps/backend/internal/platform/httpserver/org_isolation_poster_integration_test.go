//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// A poster named on an event or session must be the organization's own (or
// carry no organization at all).
func TestOrgIsolation_PosterMediaMustBelongToTheOrganization(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, orgB, "org_admin")
	tok := e.token(t, owner)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.srv.pgxPool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	media := func(org *uuid.UUID) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
		      VALUES ($1, $2, 'event_poster', 'local', $3, 'image/png', 4, repeat('0', 64))`, id, org, "poster/"+id.String())
		t.Cleanup(func() { _, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM media_objects WHERE id = $1`, id) })
		return id
	}
	foreign, own, unowned := media(&orgA), media(&orgB), media(nil)

	event := uuid.New()
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'Poster Show', 'draft', 'public')`, event, orgB)
	t.Cleanup(func() { _, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM events WHERE id = $1`, event) })

	path := fmt.Sprintf("/v1/organizations/%s/events/%s", orgB, event)
	patch := func(m uuid.UUID) string {
		return fmt.Sprintf(`{"name":"Poster Show","visibility":"public","poster_media_id":%q}`, m)
	}
	poster := func() string {
		var s string
		if err := e.srv.pgxPool.QueryRow(context.Background(),
			`SELECT COALESCE(poster_media_id::text, '-') FROM events WHERE id = $1`, event).Scan(&s); err != nil {
			t.Fatalf("read poster: %v", err)
		}
		return s
	}

	e.expect(t, 422, "PATCH", path, tok, "", patch(foreign))
	e.expect(t, 422, "PATCH", path, tok, "", patch(uuid.New()))
	if got := poster(); got != "-" {
		t.Fatalf("a refused poster was stored: %s", got)
	}
	e.expect(t, 200, "PATCH", path, tok, "", patch(own))
	if got := poster(); got != own.String() {
		t.Fatalf("own poster not stored: %s", got)
	}
	e.expect(t, 200, "PATCH", path, tok, "", patch(unowned))
}
