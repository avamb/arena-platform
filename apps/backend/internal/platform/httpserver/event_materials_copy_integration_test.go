//go:build integration

package httpserver

// Task 1-2c: copy the page materials of one event onto another event of the
// same organization, and read them next to the bundles. Run against a
// migrated database (migration 0153).

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// newEvent adds a second live event to organization org.
func (m *matEnv) newEvent(t *testing.T, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	m.exec(t, `INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, id, org, name)
	t.Cleanup(func() {
		_, _ = m.srv.pgxPool.Exec(context.Background(), `DELETE FROM events WHERE id = $1`, id)
	})
	return id
}

func (m *matEnv) copyPath(org, target, source uuid.UUID) string {
	return m.base(org, target) + "/materials/copy-from/" + source.String()
}

func (m *matEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := m.srv.pgxPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%q: %v", sql, err)
	}
	return n
}

// seedMaterials gives the event two gallery photos (one with a deleted media
// object), two videos, two partner links, a map point and one report photo.
// It returns the live gallery media id.
func (m *matEnv) seedMaterials(t *testing.T, org, event uuid.UUID) (liveMedia uuid.UUID) {
	t.Helper()
	live, dead, third := m.media(t, org, "event_gallery"), m.media(t, org, "event_gallery"), m.media(t, org, "event_gallery")
	m.exec(t, `UPDATE media_objects SET deleted_at = now() WHERE id = $1`, dead)
	m.exec(t, `INSERT INTO event_gallery_items (org_id, event_id, media_id, position, caption) VALUES
	           ($1,$2,$3,0,'Cover shot'), ($1,$2,$4,1,NULL), ($1,$2,$5,2,'Lobby')`, org, event, live, dead, third)
	m.exec(t, `INSERT INTO event_videos (org_id, event_id, provider, video_id, url, position) VALUES
	           ($1,$2,'youtube','dQw4w9WgXcQ','https://youtu.be/dQw4w9WgXcQ',1), ($1,$2,'https',NULL,'https://example.com/clip',3)`, org, event)
	m.exec(t, `INSERT INTO event_partner_links (org_id, event_id, url, seller_name, position) VALUES
	           ($1,$2,'https://a.example.com/e','A seller',0), ($1,$2,'https://b.example.com/e','B seller',2)`, org, event)
	m.exec(t, `INSERT INTO event_map_points (event_id, org_id, latitude, longitude, label) VALUES ($1,$2,50.075538,14.4378,'Main hall')`, event, org)
	m.exec(t, `INSERT INTO event_report_photos (org_id, event_id, media_id, position) VALUES ($1,$2,$3,0)`, org, event, m.media(t, org, "event_report"))
	t.Cleanup(func() {
		_, _ = m.srv.pgxPool.Exec(context.Background(), `DELETE FROM event_gallery_items WHERE org_id = $1`, org)
	})
	return live
}

func TestEventMaterialsCopy_CopiesTheFourListsNotTheReport(t *testing.T) {
	m := newMatEnv(t)
	live := m.seedMaterials(t, m.orgA, m.event)
	target := m.newEvent(t, m.orgA, "Copy Target")

	// The source's bundle read lists the four lists (not the report).
	st, out := m.do(t, http.MethodGet, m.base(m.orgA, m.event)+"/event-bundle", m.ownerTok, "", "")
	if st != http.StatusOK {
		t.Fatalf("event-bundle read: %d %s", st, out)
	}
	mat, _ := matJSON(t, out)["materials"].(map[string]any)
	if mat == nil {
		t.Fatalf("event-bundle read has no materials block: %s", out)
	}
	if g, _ := mat["gallery"].([]any); len(g) != 3 {
		t.Errorf("bundle materials.gallery = %v, want 3 rows (the organizer's view)", mat["gallery"])
	}
	if v, _ := mat["videos"].([]any); len(v) != 2 {
		t.Errorf("bundle materials.videos = %v", mat["videos"])
	}
	if p, _ := mat["partner_links"].([]any); len(p) != 2 {
		t.Errorf("bundle materials.partner_links = %v", mat["partner_links"])
	}
	if mp, _ := mat["map_point"].(map[string]any); mp == nil || mp["latitude"].(float64) != 50.075538 {
		t.Errorf("bundle materials.map_point = %v", mat["map_point"])
	}
	// A target with nothing: empty lists, null point, never null lists.
	st, out = m.do(t, http.MethodGet, m.base(m.orgA, target)+"/event-bundle", m.ownerTok, "", "")
	if st != http.StatusOK {
		t.Fatalf("target bundle read: %d %s", st, out)
	}
	empty, _ := matJSON(t, out)["materials"].(map[string]any)
	if g, ok := empty["gallery"].([]any); !ok || len(g) != 0 || empty["map_point"] != nil {
		t.Errorf("empty event materials = %v", empty)
	}

	st, out = m.do(t, http.MethodPost, m.copyPath(m.orgA, target, m.event), m.ownerTok, "", "")
	if st != http.StatusCreated {
		t.Fatalf("copy: %d %s", st, out)
	}
	res := matJSON(t, out)
	if res["event_id"] != target.String() || res["source_event_id"] != m.event.String() ||
		res["gallery"].(float64) != 2 || res["videos"].(float64) != 2 || res["partner_links"].(float64) != 2 || res["map_point"] != true {
		t.Errorf("copy answer = %v (the deleted media photo is skipped: 2 of 3)", res)
	}

	// The target now has them, positions renumbered from 0, captions kept.
	st, out = m.do(t, http.MethodGet, m.base(m.orgA, target)+"/media-items", m.ownerTok, "", "")
	if st != http.StatusOK {
		t.Fatalf("list gallery: %d %s", st, out)
	}
	gal := matPhotos(t, out)
	if len(gal) != 2 || gal[0]["media_id"] != live.String() || gal[0]["position"].(float64) != 0 || gal[0]["caption"] != "Cover shot" ||
		gal[1]["position"].(float64) != 1 || gal[1]["caption"] != "Lobby" {
		t.Errorf("target gallery = %v", gal)
	}
	if n := m.count(t, `SELECT count(*) FROM event_videos WHERE event_id=$1 AND position IN (0,1) AND url IN ('https://youtu.be/dQw4w9WgXcQ','https://example.com/clip')`, target); n != 2 {
		t.Errorf("videos copied with renumbered positions: %d, want 2", n)
	}
	if n := m.count(t, `SELECT count(*) FROM event_partner_links WHERE event_id=$1 AND position IN (0,1)`, target); n != 2 {
		t.Errorf("partner links copied with renumbered positions: %d, want 2", n)
	}
	if n := m.count(t, `SELECT count(*) FROM event_map_points WHERE event_id=$1 AND label='Main hall'`, target); n != 1 {
		t.Errorf("map point copied: %d, want 1", n)
	}
	if n := m.count(t, `SELECT count(*) FROM event_report_photos WHERE event_id=$1`, target); n != 0 {
		t.Errorf("the photo report must not be copied, got %d", n)
	}

	// Source untouched; audit written in the transaction.
	if n := m.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, m.event); n != 3 {
		t.Errorf("source gallery changed: %d", n)
	}
	if n := m.count(t, `SELECT count(*) FROM audit_events WHERE action='v1.event.materials.copy' AND resource_id=$1`, target.String()); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}

	// A repeated call is harmless: 409, nothing doubled.
	st, out = m.do(t, http.MethodPost, m.copyPath(m.orgA, target, m.event), m.ownerTok, "", "")
	if st != http.StatusConflict || sec1Code(out) != "event.materials_not_empty" {
		t.Errorf("second copy: %d %s, want 409 event.materials_not_empty", st, out)
	}
	if n := m.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, target); n != 2 {
		t.Errorf("a refused copy doubled the gallery: %d", n)
	}

	// The picture is shared, not owned: removing it from the target must not
	// touch the source row or the media object.
	itemID := gal[0]["id"].(string)
	if st, out := m.do(t, http.MethodDelete, m.base(m.orgA, target)+"/media-items/"+itemID, m.ownerTok, "", ""); st != http.StatusNoContent {
		t.Fatalf("remove from the target: %d %s", st, out)
	}
	if n := m.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1 AND media_id=$2`, m.event, live); n != 1 {
		t.Errorf("removing the copy took the source's row with it")
	}
	if n := m.count(t, `SELECT count(*) FROM media_objects WHERE id=$1 AND deleted_at IS NULL`, live); n != 1 {
		t.Errorf("removing the copy deleted the shared media object")
	}
}

func TestEventMaterialsCopy_Refusals(t *testing.T) {
	m := newMatEnv(t)
	m.seedMaterials(t, m.orgA, m.event)

	// The event onto itself.
	if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgA, m.event, m.event), m.ownerTok, "", ""); st != http.StatusBadRequest || sec1Code(out) != "event.materials_copy_same_event" {
		t.Errorf("self copy: %d %s", st, out)
	}
	// Unknown source, malformed source, deleted source.
	target := m.newEvent(t, m.orgA, "Refusal Target")
	for name, src := range map[string]string{"unknown": uuid.NewString(), "malformed": "not-a-uuid"} {
		st, out := m.do(t, http.MethodPost, m.base(m.orgA, target)+"/materials/copy-from/"+src, m.ownerTok, "", "")
		if st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
			t.Errorf("%s source: %d %s, want 404 event.not_found", name, st, out)
		}
	}
	gone := m.newEvent(t, m.orgA, "Deleted Source")
	m.exec(t, `UPDATE events SET deleted_at = now() WHERE id = $1`, gone)
	if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgA, target, gone), m.ownerTok, "", ""); st != http.StatusNotFound {
		t.Errorf("deleted source: %d %s, want 404", st, out)
	}
	// Unknown target.
	if st, _ := m.do(t, http.MethodPost, m.copyPath(m.orgA, uuid.New(), m.event), m.ownerTok, "", ""); st != http.StatusNotFound {
		t.Errorf("unknown target: %d, want 404", st)
	}
	// A target that already has ONE kind of material (a map point) refuses.
	m.exec(t, `INSERT INTO event_map_points (event_id, org_id, latitude, longitude) VALUES ($1,$2,1,2)`, target, m.orgA)
	if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgA, target, m.event), m.ownerTok, "", ""); st != http.StatusConflict || sec1Code(out) != "event.materials_not_empty" {
		t.Errorf("target with a map point: %d %s", st, out)
	}
	if n := m.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, target); n != 0 {
		t.Errorf("a refused copy left %d gallery rows behind", n)
	}
	// An empty source copies nothing and still answers 201.
	bare, bareTarget := m.newEvent(t, m.orgA, "Bare Source"), m.newEvent(t, m.orgA, "Bare Target")
	st, out := m.do(t, http.MethodPost, m.copyPath(m.orgA, bareTarget, bare), m.ownerTok, "", "")
	if st != http.StatusCreated {
		t.Fatalf("empty source: %d %s", st, out)
	}
	if r := matJSON(t, out); r["gallery"].(float64) != 0 || r["map_point"] != false {
		t.Errorf("empty source answer = %v", r)
	}
}

// Two callers copy into the same empty event at once: exactly one wins.
func TestEventMaterialsCopy_RaceCopiesOnce(t *testing.T) {
	m := newMatEnv(t)
	m.seedMaterials(t, m.orgA, m.event)
	target := m.newEvent(t, m.orgA, "Race Target")
	codes := make(chan int, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, _ := m.do(t, http.MethodPost, m.copyPath(m.orgA, target, m.event), m.ownerTok, "", "")
			codes <- st
		}()
	}
	wg.Wait()
	close(codes)
	created, conflict := 0, 0
	for st := range codes {
		switch st {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("unexpected status %d", st)
		}
	}
	if created != 1 || conflict != 5 {
		t.Errorf("created=%d conflict=%d, want 1 and 5", created, conflict)
	}
	if n := m.count(t, `SELECT count(*) FROM event_partner_links WHERE event_id=$1`, target); n != 2 {
		t.Errorf("partner links after the race = %d, want 2", n)
	}
}

func TestEventMaterialsCopy_StaysInsideTheOrganization(t *testing.T) {
	m := newMatEnv(t)
	m.seedMaterials(t, m.orgA, m.event)
	targetA := m.newEvent(t, m.orgA, "A target")
	eventB := m.newEvent(t, m.orgB, "B event")
	m.seedMaterials(t, m.orgB, eventB)

	ownerB := m.user(t, "copy-owner-b")
	m.member(t, ownerB, m.orgB, "org_admin")
	tokB := m.token(t, ownerB)
	keyB := m.key(t, m.orgB, "event.read", "event.update")
	agent := m.user(t, "copy-agent")
	m.member(t, agent, m.orgA, "agent")
	agentTok := m.token(t, agent)
	readKeyA := m.key(t, m.orgA, "event.read")

	// Organization A's owner naming B's event as the SOURCE: not found, and
	// nothing of B reaches A.
	if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgA, targetA, eventB), m.ownerTok, "", ""); st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
		t.Errorf("foreign source: %d %s, want 404 event.not_found", st, out)
	}
	if n := m.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, targetA); n != 0 {
		t.Errorf("a foreign source copied %d photos into A", n)
	}
	// B's owner and key reaching for A's event as the TARGET through B's path
	// (404: not B's event), and with A's event as the source of B's target.
	targetB := m.newEvent(t, m.orgB, "B target")
	for _, bearer := range []string{tokB, keyB} {
		if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgB, m.event, eventB), bearer, "", ""); st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
			t.Errorf("A's event as B's target: %d %s, want 404 event.not_found", st, out)
		}
		if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgB, targetB, m.event), bearer, "", ""); st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
			t.Errorf("A's event as B's source: %d %s, want 404 event.not_found", st, out)
		}
		// Through A's path they are not members at all.
		if st, _ := m.do(t, http.MethodPost, m.copyPath(m.orgA, targetA, m.event), bearer, "", ""); st != http.StatusForbidden {
			t.Errorf("B's caller on A's path: %d, want 403", st)
		}
	}
	if n := m.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, targetB); n != 0 {
		t.Errorf("A's materials reached B's event: %d photos", n)
	}
	// An agent holds no event.update; a read-only key neither.
	if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgA, targetA, m.event), agentTok, "", ""); st != http.StatusForbidden || sec1Code(out) != "permissions.denied" {
		t.Errorf("agent: %d %s, want 403 permissions.denied", st, out)
	}
	if st, _ := m.do(t, http.MethodPost, m.copyPath(m.orgA, targetA, m.event), readKeyA, "", ""); st != http.StatusForbidden {
		t.Errorf("read-only key: %d, want 403", st)
	}
	if n := m.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, targetA); n != 0 {
		t.Errorf("a refused caller copied %d photos", n)
	}
	// The happy path as A's owner still works afterwards.
	if st, out := m.do(t, http.MethodPost, m.copyPath(m.orgA, targetA, m.event), m.ownerTok, "", ""); st != http.StatusCreated {
		t.Fatalf("owner copy: %d %s", st, fmt.Sprint(out))
	}
}
