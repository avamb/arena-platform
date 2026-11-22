//go:build integration

package httpserver

// Spec 63 section 16.4: the organizer API of an event's content translations
// and the machine translation job. Run against a migrated database (0219).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/contenttranslate"
)

type trEnv struct {
	*sec1Env
	orgA, orgB uuid.UUID
	tok        string
	event      uuid.UUID // event of orgA
	foreign    uuid.UUID // event of orgB
}

func newTrEnv(t *testing.T, adjust func(*config.Config)) *trEnv {
	t.Helper()
	e := newSec1EnvCfg(t, adjust)
	m := &trEnv{sec1Env: e, orgA: e.org(t, "TA"), orgB: e.org(t, "TB"), event: uuid.New(), foreign: uuid.New()}
	owner := e.user(t, "tr-owner")
	e.member(t, owner, m.orgA, "org_admin")
	m.tok = e.token(t, owner)
	for _, ev := range []struct{ id, org uuid.UUID }{{m.event, m.orgA}, {m.foreign, m.orgB}} {
		if _, err := e.srv.pgxPool.Exec(context.Background(), `INSERT INTO events (id, org_id, name, status, visibility, description, description_format)
		    VALUES ($1, $2, 'Moon Festival', 'published', 'public', 'Come to Moon Festival', 'text')`, ev.id, ev.org); err != nil {
			t.Fatalf("event fixture: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM events WHERE id = ANY($1)`, []uuid.UUID{m.event, m.foreign})
	})
	return m
}

func (m *trEnv) path(org, event uuid.UUID, tail string) string {
	return fmt.Sprintf("/v1/organizations/%s/events/%s%s", org, event, tail)
}

func TestTranslations_ManualThenMachineJobKeepsManual(t *testing.T) {
	m := newTrEnv(t, nil)
	put := m.path(m.orgA, m.event, "/translations/cs")
	st, out := m.do(t, http.MethodPut, put, m.tok, "", `{"short_description":"Rucni kratky text","description_html":"<p>Rucni popis</p><script>alert(1)</script>"}`)
	if st != http.StatusOK {
		t.Fatalf("PUT: %d %s", st, out)
	}
	body := matJSON(t, out)
	if body["source"] != "manual" || body["translation_stale"] != false || strings.Contains(out, "alert(1)") {
		t.Fatalf("PUT answer = %s", out)
	}

	// The machine job (fake translator) writes de and leaves the manual cs alone.
	tr := &trFake{}
	err := contenttranslate.Run(context.Background(), contenttranslate.PGStore{DB: m.srv.pgxPool}, tr,
		contenttranslate.Payload{OwnerType: contenttranslate.OwnerEvent, OwnerID: m.event, OrgID: m.orgA, Locales: []string{"cs", "de"}},
		m.srv.logger)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.calls) != 1 || tr.calls[0] != "de" {
		t.Fatalf("translator calls = %v; want only de", tr.calls)
	}
	st, out = m.do(t, http.MethodGet, m.path(m.orgA, m.event, "/translations"), m.tok, "", "")
	if st != http.StatusOK {
		t.Fatalf("GET: %d %s", st, out)
	}
	list := matJSON(t, out)
	rows, _ := list["translations"].([]any)
	sources := map[string]string{}
	for _, r := range rows {
		row := r.(map[string]any)
		sources[row["locale"].(string)] = row["source"].(string)
		if row["locale"] == "cs" && row["short_description"] != "Rucni kratky text" {
			t.Errorf("manual cs text changed: %v", row)
		}
	}
	if sources["cs"] != "manual" || sources["de"] != "machine" || len(rows) != 2 {
		t.Errorf("sources = %v", sources)
	}
	if list["content_locale"] != "en" || list["machine_available"] != false {
		t.Errorf("header = %v", list)
	}

	// The original description changes: both translations are now stale.
	if _, err := m.srv.pgxPool.Exec(context.Background(), `UPDATE events SET description = 'Come to Moon Festival tomorrow' WHERE id = $1`, m.event); err != nil {
		t.Fatal(err)
	}
	_, out = m.do(t, http.MethodGet, m.path(m.orgA, m.event, "/translations"), m.tok, "", "")
	for _, r := range matJSON(t, out)["translations"].([]any) {
		if r.(map[string]any)["translation_stale"] != true {
			t.Errorf("translation not stale after the original changed: %v", r)
		}
	}

	// DELETE removes the manual row; a second DELETE is 404.
	m.expect(t, http.StatusNoContent, http.MethodDelete, put, m.tok, "", "")
	st, out = m.do(t, http.MethodDelete, put, m.tok, "", "")
	if st != http.StatusNotFound || sec1Code(out) != "translation.not_found" {
		t.Errorf("second DELETE: %d %s", st, out)
	}
}

type trFake struct{ calls []string }

func (f *trFake) Translate(_ context.Context, in contenttranslate.TranslateInput) (contenttranslate.Fields, error) {
	f.calls = append(f.calls, in.To)
	d := "Prijdte na Moon Festival"
	return contenttranslate.Fields{DescriptionHTML: &d}, nil
}

func TestTranslations_PutValidation(t *testing.T) {
	m := newTrEnv(t, nil)
	for _, loc := range []string{"en", "xx", "zz-ZZ"} {
		st, out := m.do(t, http.MethodPut, m.path(m.orgA, m.event, "/translations/"+loc), m.tok, "", `{"short_description":"x"}`)
		if st != http.StatusUnprocessableEntity || sec1Code(out) != "translation.invalid_locale" {
			t.Errorf("locale %q: %d %s", loc, st, out)
		}
	}
	long := strings.Repeat("x", 501)
	st, out := m.do(t, http.MethodPut, m.path(m.orgA, m.event, "/translations/cs"), m.tok, "", `{"short_description":"`+long+`"}`)
	if st != http.StatusUnprocessableEntity || sec1Code(out) != "translation.invalid_field" {
		t.Errorf("long short description: %d %s", st, out)
	}
	if st, _ := m.do(t, http.MethodPut, m.path(m.orgA, m.event, "/translations/cs"), m.tok, "", `{"nope":1}`); st != http.StatusBadRequest {
		t.Errorf("unknown member: %d", st)
	}
}

func TestTranslations_MachineWithoutKeyIs503(t *testing.T) {
	m := newTrEnv(t, nil)
	st, out := m.do(t, http.MethodPost, m.path(m.orgA, m.event, "/translations/machine"), m.tok, "", `{"locales":["cs"]}`)
	if st != http.StatusServiceUnavailable || sec1Code(out) != "translation.not_configured" {
		t.Fatalf("no key: %d %s", st, out)
	}
}

func TestTranslations_MachineWithKeyQueuesJob(t *testing.T) {
	m := newTrEnv(t, func(c *config.Config) { c.TranslateLLMAPIKey = "sk-ant-test-stub" })
	st, out := m.do(t, http.MethodPost, m.path(m.orgA, m.event, "/translations/machine"), m.tok, "", `{"locales":["cs","CS","de"]}`)
	if st != http.StatusAccepted {
		t.Fatalf("queue: %d %s", st, out)
	}
	body := matJSON(t, out)
	id, _ := body["job_id"].(string)
	if id == "" || len(body["locales"].([]any)) != 2 {
		t.Fatalf("answer = %s", out)
	}
	t.Cleanup(func() {
		_, _ = m.srv.pgxPool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE id::text = $1`, id)
	})
	var jobType string
	if err := m.srv.pgxPool.QueryRow(context.Background(), `SELECT job_type FROM worker_jobs WHERE id::text = $1`, id).Scan(&jobType); err != nil || jobType != contenttranslate.JobType {
		t.Errorf("job row: %q %v", jobType, err)
	}
	for _, bad := range []string{`{"locales":[]}`, `{"locales":["xx"]}`, `{"locales":["en"]}`} {
		st, out := m.do(t, http.MethodPost, m.path(m.orgA, m.event, "/translations/machine"), m.tok, "", bad)
		if st != http.StatusUnprocessableEntity || sec1Code(out) != "translation.invalid_locale" {
			t.Errorf("%s: %d %s", bad, st, out)
		}
	}
}

func TestTranslations_ForeignEventIs404(t *testing.T) {
	m := newTrEnv(t, func(c *config.Config) { c.TranslateLLMAPIKey = "sk-ant-test-stub" })
	for _, c := range []struct{ method, tail, body string }{
		{http.MethodGet, "/translations", ""},
		{http.MethodPut, "/translations/cs", `{"short_description":"x"}`},
		{http.MethodDelete, "/translations/cs", ""},
		{http.MethodPost, "/translations/machine", `{"locales":["cs"]}`},
		{http.MethodPut, "/report-text", `{"report_text_html":"<p>x</p>"}`},
	} {
		st, out := m.do(t, c.method, m.path(m.orgA, m.foreign, c.tail), m.tok, "", c.body)
		if st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
			t.Errorf("%s %s: %d %s", c.method, c.tail, st, out)
		}
	}
}

func TestTranslations_ReportText(t *testing.T) {
	m := newTrEnv(t, nil)
	st, out := m.do(t, http.MethodPut, m.path(m.orgA, m.event, "/report-text"), m.tok, "", `{"report_text_html":"<p>Report</p><script>alert(2)</script>"}`)
	if st != http.StatusOK || strings.Contains(out, "alert(2)") || !strings.Contains(out, "Report") {
		t.Fatalf("PUT report-text: %d %s", st, out)
	}
	_, out = m.do(t, http.MethodGet, m.path(m.orgA, m.event, "/translations"), m.tok, "", "")
	if got := matJSON(t, out)["original"].(map[string]any)["report_text_html"]; got == nil {
		t.Errorf("original report text missing: %s", out)
	}
	st, out = m.do(t, http.MethodPut, m.path(m.orgA, m.event, "/report-text"), m.tok, "", `{"report_text_html":null}`)
	if st != http.StatusOK || matJSON(t, out)["report_text_html"] != nil {
		t.Errorf("clear: %d %s", st, out)
	}
}
