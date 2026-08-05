// public_feed_ab47c_test.go — unit tests for AB-47c projection helpers
// (feature #436): resolution of the poster cover (session ?? event) and
// projection of the per-session media gallery into the widget contract.
package hfeed

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestPublicFeedEventFromRow_PosterCover_AB47c verifies that an event row
// with poster_media_id set surfaces poster_url built via mediaFileURL.
func TestPublicFeedEventFromRow_PosterCover_AB47c(t *testing.T) {
	t.Parallel()
	mediaID := uuid.New()
	orgID := uuid.New()
	e := gen.EventRow{
		ID:            uuid.New(),
		OrgID:         orgID,
		Name:          "AB-47c Test",
		Status:        "published",
		Visibility:    "public",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		PosterMediaID: &mediaID,
	}
	resp := publicFeedEventFromRow(e)
	if resp.PosterMediaID == nil || *resp.PosterMediaID != mediaID.String() {
		t.Fatalf("PosterMediaID: got %v, want %s", resp.PosterMediaID, mediaID)
	}
	want := "/v1/media-files/" + mediaID.String()
	if resp.PosterURL == nil || *resp.PosterURL != want {
		t.Fatalf("PosterURL: got %v, want %s", resp.PosterURL, want)
	}
}

// TestPublicFeedEventFromRow_NoPoster_AB47c: event without a cover
// exposes poster_media_id/poster_url as null (widget must treat as no
// fallback available and hide the cover element).
func TestPublicFeedEventFromRow_NoPoster_AB47c(t *testing.T) {
	t.Parallel()
	e := gen.EventRow{
		ID:         uuid.New(),
		OrgID:      uuid.New(),
		Name:       "AB-47c No Cover",
		Status:     "published",
		Visibility: "public",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	resp := publicFeedEventFromRow(e)
	if resp.PosterMediaID != nil {
		t.Errorf("PosterMediaID: want nil, got %v", *resp.PosterMediaID)
	}
	if resp.PosterURL != nil {
		t.Errorf("PosterURL: want nil, got %v", *resp.PosterURL)
	}
}

// TestPublicFeedSession_PosterFallback_AB47c verifies the "session ??
// event" resolution: when the session carries its own poster_media_id
// it wins; when it doesn't and the event supplies a fallback, that is
// used; when neither is present the field stays nil.
func TestPublicFeedSession_PosterFallback_AB47c(t *testing.T) {
	t.Parallel()
	sessionPoster := uuid.New()
	eventPoster := uuid.New()

	buildRow := func(sessionCover *uuid.UUID) gen.SessionRow {
		return gen.SessionRow{
			ID:            uuid.New(),
			EventID:       uuid.New(),
			VenueID:       uuid.New(),
			StartAt:       time.Now().Add(24 * time.Hour),
			EndAt:         time.Now().Add(27 * time.Hour),
			CapacityTotal: 100,
			Status:        "published",
			AdmissionMode: "general_admission",
			Currency:      "EUR",
			CurrencySource: "override",
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
			PosterMediaID: sessionCover,
		}
	}

	// Case A: session cover set → session wins, fallback is a no-op.
	sessResp := publicFeedSessionFromRow(buildRow(&sessionPoster), nil)
	ep := eventPoster.String()
	epURL := "/v1/media-files/" + ep
	sessResp.applyPosterFallback(&ep, &epURL)
	if sessResp.PosterMediaID == nil || *sessResp.PosterMediaID != sessionPoster.String() {
		t.Errorf("Case A session wins: got %v, want %s", sessResp.PosterMediaID, sessionPoster)
	}
	if sessResp.PosterURL == nil || *sessResp.PosterURL != "/v1/media-files/"+sessionPoster.String() {
		t.Errorf("Case A PosterURL: got %v", sessResp.PosterURL)
	}

	// Case B: no session cover, event has one → event fallback wins.
	sessResp = publicFeedSessionFromRow(buildRow(nil), nil)
	sessResp.applyPosterFallback(&ep, &epURL)
	if sessResp.PosterMediaID == nil || *sessResp.PosterMediaID != ep {
		t.Errorf("Case B event fallback: got %v, want %s", sessResp.PosterMediaID, ep)
	}
	if sessResp.PosterURL == nil || *sessResp.PosterURL != epURL {
		t.Errorf("Case B PosterURL: got %v", sessResp.PosterURL)
	}

	// Case C: neither → both fields stay nil (widget hides the cover).
	sessResp = publicFeedSessionFromRow(buildRow(nil), nil)
	sessResp.applyPosterFallback(nil, nil)
	if sessResp.PosterMediaID != nil || sessResp.PosterURL != nil {
		t.Errorf("Case C no cover: got %v / %v, want nil / nil",
			sessResp.PosterMediaID, sessResp.PosterURL)
	}
}

// TestPublicFeedSession_MediaGallery_AB47c verifies the projection from
// gen.SessionMediaItemRow to publicFeedMediaItem: poster rows expose a
// poster_url built from media_id, video rows expose video_url, and
// position is preserved.
func TestPublicFeedSession_MediaGallery_AB47c(t *testing.T) {
	t.Parallel()
	posterID := uuid.New()
	videoURL := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	rows := []gen.SessionMediaItemRow{
		{
			ID:       uuid.New(),
			Kind:     "poster",
			MediaID:  &posterID,
			Position: 0,
		},
		{
			ID:       uuid.New(),
			Kind:     "video",
			VideoURL: &videoURL,
			Position: 1,
		},
	}
	var resp publicFeedSessionResponse
	resp.applyMediaGallery(rows)
	if len(resp.MediaGallery) != 2 {
		t.Fatalf("MediaGallery len: got %d, want 2", len(resp.MediaGallery))
	}
	poster := resp.MediaGallery[0]
	if poster.Kind != "poster" || poster.Position != 0 {
		t.Errorf("poster: got %+v", poster)
	}
	wantPosterURL := "/v1/media-files/" + posterID.String()
	if poster.PosterURL == nil || *poster.PosterURL != wantPosterURL {
		t.Errorf("poster.PosterURL: got %v, want %s", poster.PosterURL, wantPosterURL)
	}
	if poster.VideoURL != nil {
		t.Errorf("poster.VideoURL: want nil, got %v", *poster.VideoURL)
	}
	video := resp.MediaGallery[1]
	if video.Kind != "video" || video.Position != 1 {
		t.Errorf("video: got %+v", video)
	}
	if video.VideoURL == nil || *video.VideoURL != videoURL {
		t.Errorf("video.VideoURL: got %v, want %s", video.VideoURL, videoURL)
	}
	if video.PosterURL != nil {
		t.Errorf("video.PosterURL: want nil, got %v", *video.PosterURL)
	}
}

// TestPublicFeedSession_MediaGallery_EmptyRows_AB47c verifies that
// passing an empty slice leaves the MediaGallery field untouched
// (default empty from publicFeedSessionFromRow).
func TestPublicFeedSession_MediaGallery_EmptyRows_AB47c(t *testing.T) {
	t.Parallel()
	sessResp := publicFeedSessionFromRow(gen.SessionRow{
		ID:             uuid.New(),
		EventID:        uuid.New(),
		VenueID:        uuid.New(),
		StartAt:        time.Now(),
		EndAt:          time.Now().Add(time.Hour),
		Status:         "published",
		AdmissionMode:  "general_admission",
		Currency:       "EUR",
		CurrencySource: "override",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}, nil)
	sessResp.applyMediaGallery(nil)
	if sessResp.MediaGallery == nil {
		t.Errorf("MediaGallery should be an empty slice (JSON []) after applyMediaGallery(nil), not nil")
	}
	if len(sessResp.MediaGallery) != 0 {
		t.Errorf("MediaGallery: got %d rows, want 0", len(sessResp.MediaGallery))
	}
}
