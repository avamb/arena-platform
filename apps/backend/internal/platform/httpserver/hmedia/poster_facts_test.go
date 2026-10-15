package hmedia

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
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// fakePosterStore is the media lookup and, as its own Storage, the byte
// source; only Get of the storage is ever called.
type fakePosterStore struct {
	obj         mediastore.Object
	getErr      error
	bytes       []byte
	storageGets int
}

type fakeBytes struct {
	storage.Storage // only Get is used
	owner           *fakePosterStore
}

func (f fakeBytes) Get(context.Context, string) (*storage.GetResult, error) {
	f.owner.storageGets++
	return &storage.GetResult{Body: io.NopCloser(bytes.NewReader(f.owner.bytes))}, nil
}

func (f *fakePosterStore) GetByID(_ context.Context, id uuid.UUID) (mediastore.Object, error) {
	if f.getErr != nil {
		return mediastore.Object{}, f.getErr
	}
	if id != f.obj.ID {
		return mediastore.Object{}, mediastore.ErrNotFound
	}
	return f.obj, nil
}

func (f *fakePosterStore) Storage() storage.Storage { return fakeBytes{owner: f} }

type fakeReader struct {
	calls    int
	gotBytes int
	gotType  string
	facts    posterread.Facts
	err      error
}

func (f *fakeReader) Read(_ context.Context, image []byte, mediaType string) (posterread.Facts, error) {
	f.calls++
	f.gotBytes = len(image)
	f.gotType = mediaType
	return f.facts, f.err
}

type posterRig struct {
	orgID  uuid.UUID
	mediaI uuid.UUID
	store  *fakePosterStore
	reader *fakeReader
	h      *Handler
	allow  *bool
}

func newPosterRig(t *testing.T) *posterRig {
	t.Helper()
	orgID, mediaID := uuid.New(), uuid.New()
	img := bytes.Repeat([]byte{1}, 2048)
	rig := &posterRig{
		orgID:  orgID,
		mediaI: mediaID,
		store: &fakePosterStore{
			obj:   mediastore.Object{ID: mediaID, OrgID: &orgID, ContentType: "image/png", ByteSize: int64(len(img)), StorageKey: "k"},
			bytes: img,
		},
		reader: &fakeReader{facts: posterread.Facts{Name: "Концерт", Date: "2099-12-15", Time: "20:00",
			Categories: []posterread.Category{{Name: "Партер", PriceMinor: 3490, Currency: "EUR"}}}},
	}
	allowed := true
	rig.allow = &allowed
	rig.h = New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithRowOrgAccess(func(w http.ResponseWriter, _ *http.Request, _ uuid.UUID, write bool, code, _ string) bool {
			if !write {
				t.Errorf("poster reading must be a write for the organization guard")
			}
			if !*rig.allow {
				http.Error(w, `{"error":{"code":"`+code+`"}}`, http.StatusNotFound)
				return false
			}
			return true
		}).
		WithPosterReader(rig.reader, nil).
		withPosterStore(rig.store)
	return rig
}

func (rig *posterRig) call(orgID, mediaID string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/organizations/{org_id}/media/{media_id}/poster-facts", rig.h.PosterFacts)
	req := httptest.NewRequest(http.MethodPost, "/organizations/"+orgID+"/media/"+mediaID+"/poster-facts", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not an error envelope: %q", rec.Body.String())
	}
	return env.Error.Code
}

func TestPosterFacts_ReadsAnOwnPosterAndAnswersSnakeCaseFacts(t *testing.T) {
	rig := newPosterRig(t)
	rec := rig.call(rig.orgID.String(), rig.mediaI.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rig.reader.calls != 1 || rig.reader.gotBytes != 2048 || rig.reader.gotType != "image/png" {
		t.Errorf("reader saw calls=%d bytes=%d type=%q", rig.reader.calls, rig.reader.gotBytes, rig.reader.gotType)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, a reading must not be cached", cc)
	}
	body := rec.Body.String()
	for _, want := range []string{`"facts"`, `"name":"Концерт"`, `"venue_name"`, `"price_minor":3490`, `"currency":"EUR"`} {
		if !strings.Contains(body, want) {
			t.Errorf("answer lacks %s: %s", want, body)
		}
	}
}

func TestPosterFacts_EmptyReadingStillAnswersAnEmptyCategoryArray(t *testing.T) {
	rig := newPosterRig(t)
	rig.reader.facts = posterread.Facts{}
	rec := rig.call(rig.orgID.String(), rig.mediaI.String())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"categories":[]`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPosterFacts_NotConfiguredIs503AndTouchesNothing(t *testing.T) {
	rig := newPosterRig(t)
	rig.h.WithPosterReader(nil, nil)
	rec := rig.call(rig.orgID.String(), rig.mediaI.String())
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "media.poster_reader_not_configured" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rig.store.storageGets != 0 {
		t.Error("an unconfigured route must not read the bytes")
	}
}

func TestPosterFacts_ReaderErrNotConfiguredIs503(t *testing.T) {
	rig := newPosterRig(t)
	rig.reader.err = posterread.ErrNotConfigured
	rec := rig.call(rig.orgID.String(), rig.mediaI.String())
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "media.poster_reader_not_configured" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPosterFacts_ModelFailureIs503AndNeverEchoesTheModelText(t *testing.T) {
	rig := newPosterRig(t)
	rig.reader.err = errors.New("posterread: model answered 500: SECRET-MODEL-TEXT")
	rec := rig.call(rig.orgID.String(), rig.mediaI.String())
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "media.poster_read_failed" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "SECRET-MODEL-TEXT") {
		t.Errorf("the model's error text leaked: %s", rec.Body.String())
	}
}

func TestPosterFacts_ForeignMissingAndOrglessMediaAre404(t *testing.T) {
	rig := newPosterRig(t)
	other := uuid.New()
	rig.store.obj.OrgID = &other
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusNotFound || errCode(t, rec) != "media.not_found" {
		t.Errorf("foreign media: %d %s", rec.Code, rec.Body.String())
	}
	rig.store.obj.OrgID = nil
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusNotFound {
		t.Errorf("org-less media: %d %s", rec.Code, rec.Body.String())
	}
	rig = newPosterRig(t)
	if rec := rig.call(rig.orgID.String(), uuid.NewString()); rec.Code != http.StatusNotFound {
		t.Errorf("unknown media: %d %s", rec.Code, rec.Body.String())
	}
	if rig.reader.calls != 0 {
		t.Error("the model must not be called for a row the caller may not see")
	}
}

func TestPosterFacts_TheOrganizationGuardDecidesFirst(t *testing.T) {
	rig := newPosterRig(t)
	*rig.allow = false
	rec := rig.call(rig.orgID.String(), rig.mediaI.String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("guard refusal must answer its own 404, got %d", rec.Code)
	}
	if rig.reader.calls != 0 {
		t.Error("the model must not be called after a refused guard")
	}
	// No guard wired at all fails closed.
	rig = newPosterRig(t)
	rig.h.WithRowOrgAccess(nil)
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no guard: %d", rec.Code)
	}
}

func TestPosterFacts_BadPathParamsAre400(t *testing.T) {
	rig := newPosterRig(t)
	if rec := rig.call("nope", rig.mediaI.String()); rec.Code != http.StatusBadRequest {
		t.Errorf("bad org id: %d", rec.Code)
	}
	if rec := rig.call(rig.orgID.String(), "nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad media id: %d", rec.Code)
	}
}

func TestPosterFacts_OnlyJPEGAndPNGAndAtMost5MiB(t *testing.T) {
	rig := newPosterRig(t)
	rig.store.obj.ContentType = "image/webp"
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "media.poster_unsupported_type" {
		t.Errorf("webp: %d %s", rec.Code, rec.Body.String())
	}
	rig.store.obj.ContentType = "image/JPEG"
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusOK || rig.reader.gotType != "image/jpeg" {
		t.Errorf("JPEG in capitals must pass as image/jpeg: %d type=%q", rec.Code, rig.reader.gotType)
	}
	rig.store.obj.ByteSize = posterMaxBytes + 1
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "media.poster_too_large" {
		t.Errorf("too large by row: %d %s", rec.Code, rec.Body.String())
	}
	// A row that understates its size is caught by the bytes themselves.
	rig.store.obj.ByteSize = 10
	rig.store.bytes = bytes.Repeat([]byte{1}, posterMaxBytes+10)
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "media.poster_too_large" {
		t.Errorf("too large by bytes: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPosterFacts_DailyCapPerOrganization429(t *testing.T) {
	rig := newPosterRig(t)
	lim := ratelimit.New(ratelimit.Config{MaxAttempts: 2, Window: PosterReadsWindow})
	rig.h.WithPosterReader(rig.reader, lim)
	for i := 0; i < 2; i++ {
		if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusOK {
			t.Fatalf("read %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := rig.call(rig.orgID.String(), rig.mediaI.String())
	if rec.Code != http.StatusTooManyRequests || errCode(t, rec) != "media.poster_reads_exhausted" {
		t.Fatalf("third read: %d %s", rec.Code, rec.Body.String())
	}
	if rig.reader.calls != 2 {
		t.Errorf("the model was called %d times, want 2", rig.reader.calls)
	}
	// A refused (429) request takes no slot either: asking again after the
	// cap must not push the organization's window further out.
	for i := 0; i < 3; i++ {
		if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("read after the cap: %d", rec.Code)
		}
	}
	if n := lim.Count(rig.orgID.String()); n != 2 {
		t.Errorf("the window holds %d reads after refused requests, want 2", n)
	}

	// Another organization has its own counter.
	otherOrg, otherMedia := uuid.New(), uuid.New()
	rig.store.obj = mediastore.Object{ID: otherMedia, OrgID: &otherOrg, ContentType: "image/png", ByteSize: 10, StorageKey: "k"}
	if rec := rig.call(otherOrg.String(), otherMedia.String()); rec.Code != http.StatusOK {
		t.Errorf("another organization must not share the cap: %d", rec.Code)
	}
}

func TestPosterFacts_ARefusedRequestTakesNoSlot(t *testing.T) {
	rig := newPosterRig(t)
	lim := ratelimit.New(ratelimit.Config{MaxAttempts: 1, Window: PosterReadsWindow})
	rig.h.WithPosterReader(rig.reader, lim)
	rig.store.obj.ContentType = "application/pdf"
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pdf: %d", rec.Code)
	}
	if lim.Count(rig.orgID.String()) != 0 {
		t.Error("a refused request took a slot of the daily cap")
	}
	rig.store.obj.ContentType = "image/png"
	if rec := rig.call(rig.orgID.String(), rig.mediaI.String()); rec.Code != http.StatusOK {
		t.Errorf("the one allowed read must still pass: %d", rec.Code)
	}
}
