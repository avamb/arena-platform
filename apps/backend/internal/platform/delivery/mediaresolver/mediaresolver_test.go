package mediaresolver

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// fakeSource stands in for *mediastore.Repo: the media_objects row and the
// signed URL are dictated by the test, while the bytes live in a real local
// storage adapter over a temp dir so the streaming path is exercised for
// real (including its Close-before-delete behaviour on Windows).
type fakeSource struct {
	obj     mediastore.Object
	getErr  error
	st      storage.Storage
	url     string
	urlErr  error
	ttlSeen time.Duration
}

func (f *fakeSource) GetByID(_ context.Context, id uuid.UUID) (mediastore.Object, error) {
	if f.getErr != nil {
		return mediastore.Object{}, f.getErr
	}
	obj := f.obj
	obj.ID = id
	return obj, nil
}

func (f *fakeSource) Storage() storage.Storage { return f.st }

func (f *fakeSource) SignedDownloadURL(_ context.Context, _ uuid.UUID, ttl time.Duration) (string, error) {
	f.ttlSeen = ttl
	if f.urlErr != nil {
		return "", f.urlErr
	}
	return f.url, nil
}

// newSourceWithBytes returns a fakeSource whose storage really holds payload
// under key, plus the key itself.
func newSourceWithBytes(t *testing.T, payload []byte) *fakeSource {
	t.Helper()
	st, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	const key = "org_logo/logo.png"
	if _, err := st.Put(context.Background(), storage.PutInput{
		Key:         key,
		ContentType: "image/png",
		Body:        bytes.NewReader(payload),
		Size:        int64(len(payload)),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return &fakeSource{obj: mediastore.Object{StorageKey: key, ContentType: "image/png"}, st: st}
}

func TestResolveLogo_ReturnsBytesAndAbsolutizesRelativeSignedURL(t *testing.T) {
	payload := []byte("\x89PNG\r\n\x1a\nlogo-bytes")
	src := newSourceWithBytes(t, payload)
	src.url = "/v1/media-files/" + uuid.NewString() + "?expires=123&sig=abc"

	r := New(src, "https://api.example.com/", nil)
	got, url, err := r.ResolveLogo(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("ResolveLogo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("bytes = %q, want %q", got, payload)
	}
	if want := "https://api.example.com" + src.url; url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if src.ttlSeen != SignedURLTTL {
		t.Errorf("signing ttl = %s, want %s", src.ttlSeen, SignedURLTTL)
	}
}

func TestResolveLogo_AbsoluteSignedURLPassesThroughUnchanged(t *testing.T) {
	src := newSourceWithBytes(t, []byte("logo"))
	src.url = "https://bucket.s3.example.com/org_logo/logo.png?X-Amz-Signature=deadbeef"

	_, url, err := New(src, "https://api.example.com", nil).
		ResolveLogo(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("ResolveLogo: %v", err)
	}
	if url != src.url {
		t.Errorf("url = %q, want the backend URL %q verbatim", url, src.url)
	}
}

func TestResolveLogo_NotFoundCasesWrapErrLogoNotFound(t *testing.T) {
	cases := map[string]struct {
		mediaID string
		mutate  func(*fakeSource)
	}{
		"media id is not a uuid": {
			mediaID: "not-a-uuid",
		},
		"row unknown or soft-deleted": {
			mediaID: uuid.NewString(),
			mutate:  func(s *fakeSource) { s.getErr = mediastore.ErrNotFound },
		},
		"row exists but the bytes are gone": {
			mediaID: uuid.NewString(),
			mutate:  func(s *fakeSource) { s.obj.StorageKey = "org_logo/vanished.png" },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src := newSourceWithBytes(t, []byte("logo"))
			if tc.mutate != nil {
				tc.mutate(src)
			}
			_, _, err := New(src, "", nil).ResolveLogo(context.Background(), tc.mediaID)
			if !errors.Is(err, delivery.ErrLogoNotFound) {
				t.Fatalf("err = %v, want it to wrap delivery.ErrLogoNotFound", err)
			}
		})
	}
}

func TestResolveLogo_StoreOutageIsNotANotFound(t *testing.T) {
	src := newSourceWithBytes(t, []byte("logo"))
	src.getErr = errors.New("s3: connection refused")

	_, _, err := New(src, "", nil).ResolveLogo(context.Background(), uuid.NewString())
	if err == nil {
		t.Fatal("ResolveLogo: want an error")
	}
	if errors.Is(err, delivery.ErrLogoNotFound) {
		t.Errorf("err = %v, want a transient error, not ErrLogoNotFound", err)
	}
}

// A signing failure must not cost the PDF its logo: the bytes still come
// back and only the e-mail's <img> is dropped.
func TestResolveLogo_SigningFailureStillReturnsBytes(t *testing.T) {
	payload := []byte("logo-bytes")
	src := newSourceWithBytes(t, payload)
	src.urlErr = errors.New("media: signing secret missing")

	got, url, err := New(src, "https://api.example.com", nil).
		ResolveLogo(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("ResolveLogo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("bytes = %q, want %q", got, payload)
	}
	if url != "" {
		t.Errorf("url = %q, want empty", url)
	}
}

// New(nil) must yield a nil INTERFACE, not a non-nil interface holding a nil
// pointer — delivery.resolveBranding/resolvePoster both branch on `m == nil`
// and would otherwise call a method on nothing.
func TestNew_NilSourceYieldsNilResolver(t *testing.T) {
	var src Source
	if got := New(src, "https://api.example.com", nil); got != nil {
		t.Fatalf("New(nil) = %#v, want a nil delivery.MediaResolver", got)
	}
}

func TestResolveLogo_OversizedObjectIsRefused(t *testing.T) {
	src := newSourceWithBytes(t, bytes.Repeat([]byte("x"), maxObjectBytes+1))

	if _, _, err := New(src, "", nil).ResolveLogo(context.Background(), uuid.NewString()); err == nil {
		t.Fatal("ResolveLogo: want an error for an object over the byte ceiling")
	}
}
