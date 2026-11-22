package contenttranslate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type fakeStore struct {
	orig    EventOriginal
	rows    map[string]Translation
	written []Translation
}

func (s *fakeStore) LoadEvent(context.Context, uuid.UUID, uuid.UUID) (EventOriginal, error) {
	return s.orig, nil
}

func (s *fakeStore) Get(_ context.Context, _ string, _ uuid.UUID, locale string) (Translation, error) {
	if t, ok := s.rows[locale]; ok {
		return t, nil
	}
	return Translation{}, pgx.ErrNoRows
}

func (s *fakeStore) PutMachine(_ context.Context, t Translation) (bool, error) {
	if ex, ok := s.rows[t.Locale]; ok && ex.Source == SourceManual {
		return false, nil
	}
	s.written = append(s.written, t)
	s.rows[t.Locale] = t
	return true, nil
}

type fakeTranslator struct {
	calls []TranslateInput
	out   func(in TranslateInput) (Fields, error)
}

func (f *fakeTranslator) Translate(_ context.Context, in TranslateInput) (Fields, error) {
	f.calls = append(f.calls, in)
	if f.out != nil {
		return f.out(in)
	}
	up := func(p *string) *string {
		if p == nil {
			return nil
		}
		s := "[" + in.To + "] " + *p
		return &s
	}
	return Fields{ShortDescription: up(in.Fields.ShortDescription), DescriptionHTML: up(in.Fields.DescriptionHTML), ReportTextHTML: up(in.Fields.ReportTextHTML)}, nil
}

func newFixture() (*fakeStore, Payload) {
	org, ev := uuid.New(), uuid.New()
	short := "A short text"
	desc := "Visit Moon Festival tonight"
	st := &fakeStore{
		orig: EventOriginal{OrgID: org, EventID: ev, ContentLocale: "en", Name: "Moon Festival",
			ShortDescription: &short, Description: &desc, DescriptionFormat: "text"},
		rows: map[string]Translation{},
	}
	return st, Payload{OwnerType: OwnerEvent, OwnerID: ev, OrgID: org, Locales: []string{"cs"}}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRun_ManualRowNeverTouched(t *testing.T) {
	st, p := newFixture()
	manual := "hand made"
	st.rows["cs"] = Translation{Locale: "cs", Source: SourceManual, SourceHash: "old", Fields: Fields{ShortDescription: &manual}}
	tr := &fakeTranslator{}
	if err := Run(context.Background(), st, tr, p, quiet()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.calls) != 0 || len(st.written) != 0 {
		t.Fatalf("manual row must not be translated or written: calls=%d written=%d", len(tr.calls), len(st.written))
	}
}

func TestRun_UnchangedMachineSkippedChangedRetranslated(t *testing.T) {
	st, p := newFixture()
	st.rows["cs"] = Translation{Locale: "cs", Source: SourceMachine, SourceHash: st.orig.Hash()}
	tr := &fakeTranslator{}
	if err := Run(context.Background(), st, tr, p, quiet()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.calls) != 0 {
		t.Fatalf("an up-to-date machine row must be skipped, got %d calls", len(tr.calls))
	}
	// The original changes: the same row is now stale and is translated again.
	changed := "Visit Moon Festival tomorrow"
	st.orig.Description = &changed
	if err := Run(context.Background(), st, tr, p, quiet()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.calls) != 1 || len(st.written) != 1 {
		t.Fatalf("a stale machine row must be retranslated once: calls=%d written=%d", len(tr.calls), len(st.written))
	}
	w := st.written[0]
	if w.Fields.Name != nil || w.SourceHash != st.orig.Hash() || w.Locale != "cs" {
		t.Errorf("written = %+v", w)
	}
}

func TestRun_ContentLocaleSkippedAndOnlyNonEmptySent(t *testing.T) {
	st, p := newFixture()
	st.orig.ShortDescription = nil
	p.Locales = []string{"en", "de"}
	tr := &fakeTranslator{}
	if err := Run(context.Background(), st, tr, p, quiet()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.calls) != 1 || tr.calls[0].To != "de" {
		t.Fatalf("calls = %+v; want one call for de", tr.calls)
	}
	in := tr.calls[0]
	if in.Fields.ShortDescription != nil || in.Fields.DescriptionHTML == nil || in.Fields.ReportTextHTML != nil {
		t.Errorf("only the non-empty description may be sent: %+v", in.Fields)
	}
}

func TestRun_AlteredNameWritesNothing(t *testing.T) {
	st, p := newFixture()
	tr := &fakeTranslator{out: func(_ TranslateInput) (Fields, error) {
		d := "Navstivte Mesicni festival dnes vecer"
		return Fields{DescriptionHTML: &d}, nil
	}}
	if err := Run(context.Background(), st, tr, p, quiet()); err != nil {
		t.Fatalf("a refused translation must not be an error (no retry): %v", err)
	}
	if len(st.written) != 0 {
		t.Fatalf("nothing may be written when the name changed: %+v", st.written)
	}
}

func TestRun_TransientErrorIsReturnedPermanentIsNot(t *testing.T) {
	st, p := newFixture()
	transient := &fakeTranslator{out: func(TranslateInput) (Fields, error) {
		return Fields{}, &APIError{Status: 529, Type: "overloaded_error"}
	}}
	if err := Run(context.Background(), st, transient, p, quiet()); err == nil {
		t.Fatal("a 529 must be returned for the job retry")
	}
	permanent := &fakeTranslator{out: func(TranslateInput) (Fields, error) { return Fields{}, &APIError{Status: 400} }}
	if err := Run(context.Background(), st, permanent, p, quiet()); err != nil {
		t.Fatalf("a 400 must not be retried: %v", err)
	}
}

func TestRun_NoTranslatorCompletesWithoutWriting(t *testing.T) {
	st, p := newFixture()
	if err := Run(context.Background(), st, nil, p, quiet()); err != nil || len(st.written) != 0 {
		t.Fatalf("err=%v written=%d", err, len(st.written))
	}
}

func TestCheckName(t *testing.T) {
	name := "Tom & Jerry"
	orig := Fields{DescriptionHTML: sp("<p>See Tom &amp; Jerry live</p>"), ShortDescription: sp("No name here")}
	ok := Fields{DescriptionHTML: sp("<p>Vidite Tom &amp; Jerry zive</p>"), ShortDescription: sp("Zadne jmeno")}
	if err := CheckName(name, orig, ok); err != nil {
		t.Errorf("name kept: %v", err)
	}
	bad := Fields{DescriptionHTML: sp("<p>Vidite Tom a Jerry zive</p>")}
	var ne *NameAlteredError
	if err := CheckName(name, orig, bad); !errors.As(err, &ne) || ne.Field != "description_html" {
		t.Errorf("altered name: %v", err)
	}
}

func TestAnthropic_TranslateSanitizesAndChecksName(t *testing.T) {
	answer := func(desc string) string {
		b := strings.NewReplacer("%DESC%", desc).Replace(`{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"return_translation","input":{"description_html":"%DESC%"}}]}`)
		return b
	}
	var gotKey string
	reply := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	a := NewAnthropic("k", "", srv.URL, nil)
	in := TranslateInput{From: "en", To: "cs", Name: "Moon Festival", Fields: Fields{DescriptionHTML: sp("<p>Moon Festival</p>")}}

	reply = answer(`<p>Moon Festival<script>alert(1)</script></p>`)
	out, err := a.Translate(context.Background(), in)
	if err != nil || gotKey != "k" {
		t.Fatalf("Translate: %v (key %q)", err, gotKey)
	}
	if out.DescriptionHTML == nil || strings.Contains(*out.DescriptionHTML, "script") || out.Name != nil {
		t.Errorf("out = %+v", out)
	}

	reply = answer(`<p>Mesicni festival</p>`)
	var ne *NameAlteredError
	if _, err := a.Translate(context.Background(), in); !errors.As(err, &ne) || !Permanent(err) {
		t.Errorf("altered name: %v", err)
	}

	if _, err := NewAnthropic("", "", srv.URL, nil).Translate(context.Background(), in); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no key: %v", err)
	}
}

func TestDecide_BlankOriginalIsNothingToTranslate(t *testing.T) {
	blank := "  "
	orig := EventOriginal{ContentLocale: "en", Name: "Night", ShortDescription: &blank}
	if got := Decide(orig, "cs", nil); got != DecisionSkipNoContent {
		t.Fatalf("Decide with a blank original = %s, want %s", got, DecisionSkipNoContent)
	}
}
