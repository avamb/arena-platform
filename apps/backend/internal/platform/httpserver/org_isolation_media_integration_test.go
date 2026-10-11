//go:build integration

package httpserver

// org_isolation_media_integration_test.go — SEC-3: /v1/media has no {org_id}
// in its path. An upload names its organization in the org_id form value and
// a media object carries its own org_id afterwards; both must be one of the
// caller's organizations, and a refusal is the route's own 404.
//
// Attackers are the SEC-2 set (org-B owner and manager JWTs with an empty
// roles claim, and an org-B API key). A media object without an organization
// stays readable, but only a platform superadmin may delete it.
//
// Run against a migrated database (AGENTS.md CI-Integration recipe).

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

type mediaSweep struct {
	*sweepEnv
	key string // org-B API key with the media scopes
}

func newMediaSweep(t *testing.T) *mediaSweep {
	t.Helper()
	e := newSweepEnv(t)
	ctx := context.Background()
	u, err := e.q.InsertUser(ctx, "media-sweep-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	_, raw, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(e.q), apikeys.IssueInput{
		OrgID: e.orgB, Name: "media-sweep-" + uuid.NewString()[:6],
		Scopes: []string{"media.read", "media.write", "media.delete"}, CreatedBy: u.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM media_objects WHERE org_id = ANY($1)`, []uuid.UUID{e.orgB, e.orgC})
	})
	return &mediaSweep{sweepEnv: e, key: raw}
}

func (m *mediaSweep) callers() []sweepCaller {
	return []sweepCaller{
		{"b-owner-jwt", m.owner, func(string) bool { return false }},
		{"b-manager-jwt", m.manager, func(p string) bool { return !m.managerPerms[p] }},
		{"b-api-key", m.key, func(string) bool { return false }},
	}
}

// seedMedia stores a media object row for org (nil = no organization).
func (m *mediaSweep) seedMedia(t *testing.T, org *uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	m.exec(t, `INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
	           VALUES ($1, $2, 'event_poster', 'local', $3, 'image/png', 4, 'sweep')`, id, org, "sweep/"+id.String())
	t.Cleanup(func() { _, _ = m.pool.Exec(context.Background(), `DELETE FROM media_objects WHERE id = $1`, id) })
	return id
}

func (m *mediaSweep) mediaState(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := m.pool.QueryRow(context.Background(),
		`SELECT COALESCE(org_id::text, '-') || ':' || (deleted_at IS NOT NULL) FROM media_objects WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("media state: %v", err)
	}
	return s
}

func (m *mediaSweep) countOrgMedia(t *testing.T, org uuid.UUID) int {
	t.Helper()
	var n int
	if err := m.pool.QueryRow(context.Background(), `SELECT count(*) FROM media_objects WHERE org_id = $1`, org).Scan(&n); err != nil {
		t.Fatalf("count media: %v", err)
	}
	return n
}

func (m *mediaSweep) upload(t *testing.T, token string, org uuid.UUID) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("owner_type", "event_poster")
	_ = mw.WriteField("org_id", org.String())
	_ = mw.WriteField("content_type", "image/png")
	fw, err := mw.CreateFormFile("file", "poster.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\nsweep"))
	_ = mw.Close()
	req, err := http.NewRequest(http.MethodPost, m.ts.URL+"/v1/media", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := m.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw := integReadBody(t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return resp.StatusCode, out
}

func errCode(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

func TestOrgIsolationMedia_ForeignMediaIsNotFoundAndNothingMoves(t *testing.T) {
	m := newMediaSweep(t)
	orgA := m.newOrg(t, "A")
	t.Cleanup(func() {
		_, _ = m.pool.Exec(context.Background(), `DELETE FROM media_objects WHERE org_id = $1`, orgA)
		_, _ = m.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgA)
	})
	for _, caller := range m.callers() {
		caller := caller
		t.Run(caller.name, func(t *testing.T) {
			media := m.seedMedia(t, &orgA)
			before := m.mediaState(t, media)
			path := "/v1/media/" + media.String()

			for _, c := range []struct {
				method, perm string
			}{{http.MethodGet, "media.read"}, {http.MethodDelete, "media.delete"}} {
				want := http.StatusNotFound
				if caller.lacks(c.perm) {
					want = http.StatusForbidden
				}
				status, out := m.do(t, c.method, path, caller.token, "")
				if status != want {
					t.Errorf("%s %s: status %d, want %d (%v)", c.method, path, status, want, out)
				}
				if want == http.StatusNotFound && errCode(out) != "media.not_found" {
					t.Errorf("%s %s: code %q, want media.not_found", c.method, path, errCode(out))
				}
			}
			if after := m.mediaState(t, media); after != before {
				t.Errorf("organization A's media moved: %s -> %s", before, after)
			}

			// An upload filed under organization A.
			n := m.countOrgMedia(t, orgA)
			want := http.StatusNotFound
			if caller.lacks("media.write") {
				want = http.StatusForbidden
			}
			status, out := m.upload(t, caller.token, orgA)
			if status != want {
				t.Errorf("upload into organization A: status %d, want %d (%v)", status, want, out)
			}
			if want == http.StatusNotFound && errCode(out) != "media.org_not_found" {
				t.Errorf("upload into organization A: code %q, want media.org_not_found", errCode(out))
			}
			if got := m.countOrgMedia(t, orgA); got != n {
				t.Errorf("upload created organization A media: %d -> %d", n, got)
			}
		})
	}
}

func TestOrgIsolationMedia_OwnOrganizationStillWorks(t *testing.T) {
	m := newMediaSweep(t)
	for _, caller := range []sweepCaller{m.callers()[0], m.callers()[2]} {
		caller := caller
		t.Run(caller.name, func(t *testing.T) {
			status, out := m.upload(t, caller.token, m.orgB)
			if status != http.StatusCreated {
				t.Fatalf("upload into own organization: status %d (%v)", status, out)
			}
			obj, _ := out["media_object"].(map[string]any)
			id, _ := obj["id"].(string)
			path := "/v1/media/" + id
			if st, out := m.do(t, http.MethodGet, path, caller.token, ""); st != http.StatusOK {
				t.Errorf("read own media: status %d (%v)", st, out)
			}
			if st, out := m.do(t, http.MethodDelete, path, caller.token, ""); st != http.StatusNoContent {
				t.Errorf("delete own media: status %d (%v)", st, out)
			}
		})
	}
}

// A media object without an organization stays readable, but nobody short of
// a platform superadmin may delete it.
func TestOrgIsolationMedia_UnownedMediaIsReadOnly(t *testing.T) {
	m := newMediaSweep(t)
	media := m.seedMedia(t, nil)
	path := "/v1/media/" + media.String()
	if st, out := m.do(t, http.MethodGet, path, m.owner, ""); st != http.StatusOK {
		t.Errorf("read unowned media: status %d (%v)", st, out)
	}
	if st, out := m.do(t, http.MethodDelete, path, m.owner, ""); st != http.StatusNotFound {
		t.Errorf("delete unowned media: status %d (%v), want 404", st, out)
	}
	if got := m.mediaState(t, media); got != "-:false" {
		t.Errorf("unowned media moved: %s", got)
	}
}
