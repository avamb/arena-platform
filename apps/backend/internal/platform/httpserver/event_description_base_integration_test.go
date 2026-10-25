//go:build integration

package httpserver

// event_description_base_integration_test.go - RT-12b review: the events routes
// resolve `description` for the request's locale (translation, English, base),
// so an editor that read the list and PATCHed `description` back wrote a
// TRANSLATION over the canonical text. `description_base` is the raw text of the
// events row and `description_translated` says the shown text is another one.
// An editor reads and writes the base; the translations stay as they were.
// Through the REAL router.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func (e *enEnv) listedEvent(t *testing.T, org uuid.UUID, tok string, ev uuid.UUID, query string) map[string]any {
	t.Helper()
	raw := e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+org.String()+"/events"+query, tok, "", "")
	var list struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Events {
		if item["id"] == ev.String() {
			return item
		}
	}
	t.Fatalf("event %s is not in the list %s", ev, raw)
	return nil
}

func wantBool(t *testing.T, m map[string]any, key string, want bool) {
	t.Helper()
	got, ok := m[key].(bool)
	if !ok {
		t.Fatalf("%s = %v (%T), want a boolean", key, m[key], m[key])
	}
	if got != want {
		t.Errorf("%s = %v, want %v", key, got, want)
	}
}

func TestEventDescriptionBase_ListAndGetCarryTheCanonicalTextNextToTheTranslation(t *testing.T) {
	e, org, tok := descEnv(t)
	base := "/v1/organizations/" + org.String() + "/events"
	canon := "<p>The <strong>original</strong> text.</p>"
	en := "<p>English <em>words</em></p>"
	ru := "<p>Русский <strong>перевод</strong></p>"
	out := e.expect(t, http.StatusCreated, http.MethodPost, base, tok, "",
		`{"name":"Base `+uuid.NewString()[:6]+`","description_format":"art","description":`+jsonString(canon)+`,
		  "translations":{"en":{"description":`+jsonString(en)+`},"ru":{"description":`+jsonString(ru)+`}}}`)
	ev := e.created(t, out)

	// Russian: the shown text is the translation, the base is the original.
	item := e.listedEvent(t, org, tok, ev, "?lang=ru")
	wantStr(t, item, "description", ru)
	wantStr(t, item, "description_base", canon)
	wantBool(t, item, "description_translated", true)
	one := e.readEvent(t, tok, ev, "?lang=ru")
	wantStr(t, one, "description", ru)
	wantStr(t, one, "description_base", canon)
	wantBool(t, one, "description_translated", true)

	// A language with no translation falls back to the English one: still not the base.
	de := e.listedEvent(t, org, tok, ev, "?lang=de")
	wantStr(t, de, "description", en)
	wantStr(t, de, "description_base", canon)
	wantBool(t, de, "description_translated", true)

	// The formats and views stay those of the shown text.
	wantStr(t, item, "description_format", "art")
	wantStr(t, item, "description_text", "Русский перевод")
}

func TestEventDescriptionBase_TheBaseOfAnEventWithoutATranslationIsTheDescription(t *testing.T) {
	e, org, tok := descEnv(t)
	base := "/v1/organizations/" + org.String() + "/events"
	canon := "<p>Only the <strong>base</strong>.</p>"
	ev := e.created(t, e.expect(t, http.StatusCreated, http.MethodPost, base, tok, "",
		`{"name":"No tr `+uuid.NewString()[:6]+`","description_format":"art","description":`+jsonString(canon)+`}`))

	item := e.listedEvent(t, org, tok, ev, "?lang=ru")
	wantStr(t, item, "description", canon)
	wantStr(t, item, "description_base", canon)
	wantBool(t, item, "description_translated", false)
}

func TestEventDescriptionBase_AnEventWithOnlyATranslationHasNoBase(t *testing.T) {
	e, org, tok := descEnv(t)
	ev := e.event(t, org) // no description of its own
	if err := e.q.UpsertEventI18nDescription(context.Background(), ev.String(), "ru", "Только перевод"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM i18n_text WHERE key = $1`, ev.String())
	})

	item := e.listedEvent(t, org, tok, ev, "?lang=ru")
	wantStr(t, item, "description", "Только перевод")
	if strField(t, item, "description_base") != nil {
		t.Errorf("description_base = %v, want null", item["description_base"])
	}
	wantBool(t, item, "description_translated", true)

	// An event with nothing at all.
	other := e.event(t, org)
	none := e.listedEvent(t, org, tok, other, "?lang=ru")
	if strField(t, none, "description") != nil || strField(t, none, "description_base") != nil {
		t.Errorf("description %v base %v, want both null", none["description"], none["description_base"])
	}
	wantBool(t, none, "description_translated", false)
}

// The mini app's path: read the list in a Russian client, write the BASE back
// with one PATCH. The translations are what they were.
func TestEventDescriptionBase_AnEditorThatWritesTheBaseLeavesTheTranslationsAlone(t *testing.T) {
	e, org, tok := descEnv(t)
	base := "/v1/organizations/" + org.String() + "/events"
	canon := "<p>The original.</p>"
	en := "<p>English words</p>"
	ru := "<p>Русский перевод</p>"
	ev := e.created(t, e.expect(t, http.StatusCreated, http.MethodPost, base, tok, "",
		`{"name":"Edit `+uuid.NewString()[:6]+`","description_format":"art","description":`+jsonString(canon)+`,
		  "translations":{"en":{"description":`+jsonString(en)+`},"ru":{"description":`+jsonString(ru)+`}}}`))

	seen := e.listedEvent(t, org, tok, ev, "?lang=ru")
	got := strField(t, seen, "description_base")
	if got == nil || *got != canon {
		t.Fatalf("the editor was handed %v as the canonical text", got)
	}
	edited := "<ul><li>The original.</li></ul>"
	e.expect(t, http.StatusOK, http.MethodPatch, base+"/"+ev.String(), tok, "",
		`{"description":`+jsonString(edited)+`,"description_format":"art"}`)

	stored, format := e.descriptionOf(t, ev)
	if stored == nil || *stored != edited || format != "art" {
		t.Fatalf("stored %v / %q, want the edited base", stored, format)
	}
	if v, _ := e.i18nDescription(t, ev, "en"); v != en {
		t.Errorf("the English translation changed: %q", v)
	}
	if v, _ := e.i18nDescription(t, ev, "ru"); v != ru {
		t.Errorf("the Russian translation changed: %q", v)
	}
	after := e.listedEvent(t, org, tok, ev, "?lang=ru")
	wantStr(t, after, "description", ru)
	wantStr(t, after, "description_base", edited)
}
