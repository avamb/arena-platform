// poster_535_test.go — unit tests for feature #535 (W1-S1a, spec
// 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.1): GET_ALL_ACTIONS must
// hand the WordPress site a poster URL it can actually download.
//
// Before #535 the wire carried a bare, host-relative `/v1/media-files/{uuid}`;
// `GET /v1/media-files/{id}` requires an `expires`/`sig` pair and answers 401
// without one, so the site's artwork sync got nothing. The catalog now renders
// media-backed posters through an injected signer producing an ABSOLUTE signed
// URL on the API public origin, while the legacy `events.image_url`
// passthrough and the no-signer fallback stay byte-identical.
package hbil24

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// fakeSigner mimics mediastore's local-backend signed URL on a given origin.
func fakeSigner(origin string) MediaURLSigner {
	return func(_ context.Context, id uuid.UUID) string {
		return origin + "/v1/media-files/" + id.String() + "?expires=4102444800&sig=deadbeef"
	}
}

// TestW1S1a_PosterURL_SignedAbsolute proves that with a signer wired both
// media-backed sources (session override and event cover) render as absolute
// URLs on the configured API origin carrying the expires+sig pair.
func TestW1S1a_PosterURL_SignedAbsolute(t *testing.T) {
	t.Parallel()
	const origin = "https://api.example.test"
	h := (&Handler{}).WithPosterSigner(fakeSigner(origin))
	ctx := context.Background()

	eventPoster := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	sessionPoster := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	legacy := "https://legacy.example.com/poster.jpg"
	row := gen.EventRow{
		ID:            uuid.New(),
		Name:          "Signed",
		Status:        "published",
		PosterMediaID: &eventPoster,
		ImageURL:      &legacy,
	}

	for _, tc := range []struct {
		name    string
		session *uuid.UUID
		wantID  uuid.UUID
	}{
		{"event cover", nil, eventPoster},
		{"session override wins", &sessionPoster, sessionPoster},
	} {
		got := h.posterURL(ctx, row, tc.session)
		if !strings.HasPrefix(got, origin+"/v1/media-files/"+tc.wantID.String()) {
			t.Fatalf("%s: posterURL = %q, want an absolute %s/v1/media-files/%s URL",
				tc.name, got, origin, tc.wantID)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("%s: posterURL %q does not parse: %v", tc.name, got, err)
		}
		if u.Scheme == "" || u.Host == "" {
			t.Errorf("%s: posterURL %q must be absolute (scheme+host), got scheme=%q host=%q",
				tc.name, got, u.Scheme, u.Host)
		}
		if u.Query().Get("expires") == "" {
			t.Errorf("%s: posterURL %q must carry an expires parameter", tc.name, got)
		}
		if u.Query().Get("sig") == "" {
			t.Errorf("%s: posterURL %q must carry a sig parameter", tc.name, got)
		}
	}
}

// TestW1S1a_PosterURL_LegacyImageURLNeverRewritten proves the pre-AB-47
// free-form column is passed through verbatim even with a signer wired — it is
// an arbitrary external URL, not a media object.
func TestW1S1a_PosterURL_LegacyImageURLNeverRewritten(t *testing.T) {
	t.Parallel()
	legacy := "https://legacy.example.com/poster.jpg"
	h := (&Handler{}).WithPosterSigner(fakeSigner("https://api.example.test"))
	got := h.posterURL(context.Background(),
		gen.EventRow{ID: uuid.New(), Name: "Legacy", Status: "published", ImageURL: &legacy}, nil)
	if got != legacy {
		t.Errorf("posterURL = %q, want the legacy image_url %q verbatim", got, legacy)
	}
}

// TestW1S1a_PosterURL_NoSignerKeepsRelative proves the pre-#535 projection is
// preserved when no signer is wired (unit tests, deployments without media
// storage) — an unsigned relative path is still better than an empty key.
func TestW1S1a_PosterURL_NoSignerKeepsRelative(t *testing.T) {
	t.Parallel()
	posterID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	h := &Handler{}
	got := h.posterURL(context.Background(),
		gen.EventRow{ID: uuid.New(), Name: "Unwired", Status: "published", PosterMediaID: &posterID}, nil)
	if want := "/v1/media-files/" + posterID.String(); got != want {
		t.Errorf("posterURL with no signer = %q, want %q", got, want)
	}
}

// TestW1S1a_PosterURL_SignerFailureFallsBack proves a signer that cannot sign
// (storage down, object missing) degrades to the relative path instead of
// emitting an empty bigPosterUrl.
func TestW1S1a_PosterURL_SignerFailureFallsBack(t *testing.T) {
	t.Parallel()
	posterID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	h := (&Handler{}).WithPosterSigner(func(context.Context, uuid.UUID) string { return "" })
	got := h.posterURL(context.Background(),
		gen.EventRow{ID: uuid.New(), Name: "Signer down", Status: "published", PosterMediaID: &posterID}, nil)
	if want := "/v1/media-files/" + posterID.String(); got != want {
		t.Errorf("posterURL with a failing signer = %q, want the relative fallback %q", got, want)
	}
}

// TestW1S1a_PosterURL_NoArtworkStaysEmpty pins the omit-the-key contract: a
// row with no artwork at all must not acquire one through the signer.
func TestW1S1a_PosterURL_NoArtworkStaysEmpty(t *testing.T) {
	t.Parallel()
	h := (&Handler{}).WithPosterSigner(fakeSigner("https://api.example.test"))
	if got := h.posterURL(context.Background(),
		gen.EventRow{ID: uuid.New(), Name: "Bare", Status: "published"}, nil); got != "" {
		t.Errorf("posterURL for a row with no artwork = %q, want \"\"", got)
	}
}
