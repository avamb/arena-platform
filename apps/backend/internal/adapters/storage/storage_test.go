package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Config / factory
// ──────────────────────────────────────────────────────────────────────────────

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "empty backend", cfg: Config{}, wantErr: "MEDIA_BACKEND is required"},
		{name: "unknown backend", cfg: Config{Backend: "gcs"}, wantErr: "invalid"},
		{name: "local missing root", cfg: Config{Backend: BackendLocal}, wantErr: "MEDIA_LOCAL_ROOT"},
		{
			name: "s3 missing fields",
			cfg: Config{
				Backend:    BackendS3,
				S3Endpoint: "https://s3.example.com",
			},
			wantErr: "MEDIA_S3_REGION",
		},
		{name: "local valid", cfg: Config{Backend: BackendLocal, LocalRoot: t.TempDir()}},
		{
			name: "s3 valid",
			cfg: Config{
				Backend:           BackendS3,
				S3Endpoint:        "https://s3.example.com",
				S3Region:          "us-east-1",
				S3Bucket:          "b",
				S3AccessKeyID:     "AKIA",
				S3SecretAccessKey: "secret",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestNewFromConfig_Local(t *testing.T) {
	t.Parallel()
	st, err := NewFromConfig(Config{Backend: BackendLocal, LocalRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if st.Backend() != BackendLocal {
		t.Fatalf("expected local backend, got %s", st.Backend())
	}
}

func TestNewFromConfig_S3(t *testing.T) {
	t.Parallel()
	st, err := NewFromConfig(Config{
		Backend:           BackendS3,
		S3Endpoint:        "https://s3.example.com",
		S3Region:          "us-east-1",
		S3Bucket:          "b",
		S3AccessKeyID:     "AKIA",
		S3SecretAccessKey: "secret",
	})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if st.Backend() != BackendS3 {
		t.Fatalf("expected s3 backend, got %s", st.Backend())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Key validation
// ──────────────────────────────────────────────────────────────────────────────

func TestValidateKey(t *testing.T) {
	t.Parallel()
	good := []string{"a", "a/b/c", "org-1/posters/2026/poster.png", "x.y_z~q"}
	for _, k := range good {
		if err := validateKey(k); err != nil {
			t.Errorf("expected %q valid, got %v", k, err)
		}
	}
	bad := []string{"", "/abs", "trail/", "a//b", "a/./b", "a/../b", "a\\b", "no\x00null"}
	for _, k := range bad {
		if err := validateKey(k); err == nil {
			t.Errorf("expected %q invalid", k)
		} else if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("expected ErrInvalidKey for %q, got %v", k, err)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// LocalStorage end-to-end
// ──────────────────────────────────────────────────────────────────────────────

func TestLocalStorage_PutGetStatDelete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	st, err := NewLocalStorage(root)
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	ctx := context.Background()
	payload := []byte("hello world")
	key := "org-1/logos/logo.png"

	obj, err := st.Put(ctx, PutInput{
		Key:         key,
		ContentType: "image/png",
		Size:        int64(len(payload)),
		Body:        bytes.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if obj.Size != int64(len(payload)) {
		t.Fatalf("expected size %d, got %d", len(payload), obj.Size)
	}

	got, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if !bytes.Equal(body, payload) {
		t.Fatalf("Get returned %q, want %q", body, payload)
	}
	if got.ContentType != "image/png" {
		t.Fatalf("Content-Type roundtrip failed: got %q", got.ContentType)
	}

	stat, err := st.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size != int64(len(payload)) {
		t.Fatalf("Stat size: got %d want %d", stat.Size, len(payload))
	}

	if err := st.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: expected ErrNotFound, got %v", err)
	}
	if err := st.Delete(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete after Delete: expected ErrNotFound, got %v", err)
	}
}

func TestLocalStorage_SizeMismatch(t *testing.T) {
	t.Parallel()
	st, _ := NewLocalStorage(t.TempDir())
	_, err := st.Put(context.Background(), PutInput{
		Key:  "k",
		Size: 100,
		Body: bytes.NewReader([]byte("short")),
	})
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("expected ErrSizeMismatch, got %v", err)
	}
}

func TestLocalStorage_RejectsBadKey(t *testing.T) {
	t.Parallel()
	st, _ := NewLocalStorage(t.TempDir())
	_, err := st.Put(context.Background(), PutInput{
		Key:  "../escape",
		Body: bytes.NewReader([]byte("x")),
	})
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// S3Storage against an httptest server
// ──────────────────────────────────────────────────────────────────────────────

// fakeS3 captures the latest request so tests can assert on signing headers
// and routing, and returns canned responses keyed by method.
type fakeS3 struct {
	t      *testing.T
	store  map[string][]byte
	header map[string]string // last seen request headers
	server *httptest.Server
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	fs := &fakeS3{t: t, store: map[string][]byte{}, header: map[string]string{}}
	fs.server = httptest.NewServer(http.HandlerFunc(fs.handle))
	t.Cleanup(fs.server.Close)
	return fs
}

func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	for k := range r.Header {
		f.header[strings.ToLower(k)] = r.Header.Get(k)
	}
	// Expect path-style requests: /bucket/key...
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) < 2 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	key := parts[1]

	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.store[key] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		data, ok := f.store[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconvI(len(data)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	case http.MethodDelete:
		if _, ok := f.store[key]; !ok {
			http.NotFound(w, r)
			return
		}
		delete(f.store, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func strconvI(n int) string {
	// Inlined to avoid importing strconv twice.
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

func newTestS3(t *testing.T, fs *fakeS3) *S3Storage {
	t.Helper()
	fixed := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	st, err := NewS3Storage(S3Options{
		Endpoint:        fs.server.URL,
		Region:          "us-east-1",
		Bucket:          "test-bucket",
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		UsePathStyle:    true,
		HTTPClient:      fs.server.Client(),
		Now:             func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	return st
}

func TestS3Storage_RoundTripAndSignatureHeaders(t *testing.T) {
	t.Parallel()
	fs := newFakeS3(t)
	st := newTestS3(t, fs)
	ctx := context.Background()
	payload := []byte("the bytes")
	key := "org-1/logos/logo.png"

	if _, err := st.Put(ctx, PutInput{
		Key:         key,
		ContentType: "image/png",
		Size:        int64(len(payload)),
		Body:        bytes.NewReader(payload),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if got := fs.header["x-amz-date"]; got != "20260630T120000Z" {
		t.Fatalf("X-Amz-Date: %q", got)
	}
	if got := fs.header["x-amz-content-sha256"]; got != sha256Hex(payload) {
		t.Fatalf("X-Amz-Content-Sha256: %q want %s", got, sha256Hex(payload))
	}
	auth := fs.header["authorization"]
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260630/us-east-1/s3/aws4_request") {
		t.Fatalf("Authorization header: %q", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=") || !strings.Contains(auth, "Signature=") {
		t.Fatalf("Authorization header missing SignedHeaders/Signature: %q", auth)
	}

	// Round-trip Get.
	got, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if !bytes.Equal(body, payload) {
		t.Fatalf("Get body mismatch")
	}

	// Stat
	stat, err := st.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size != int64(len(payload)) {
		t.Fatalf("Stat size: %d want %d", stat.Size, len(payload))
	}

	// Delete
	if err := st.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestS3Storage_NotFound(t *testing.T) {
	t.Parallel()
	fs := newFakeS3(t)
	st := newTestS3(t, fs)
	if _, err := st.Stat(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := st.Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestS3Storage_PathStyleURL(t *testing.T) {
	t.Parallel()
	st, _ := NewS3Storage(S3Options{
		Endpoint:        "https://s3.example.com",
		Region:          "us-east-1",
		Bucket:          "bkt",
		AccessKeyID:     "id",
		SecretAccessKey: "secret",
		UsePathStyle:    true,
	})
	u := st.urlForKey("a/b c")
	if u.Path != "/bkt/a/b%20c" {
		t.Fatalf("path-style url path: %q", u.Path)
	}
	if u.Host != "s3.example.com" {
		t.Fatalf("path-style url host: %q", u.Host)
	}
}

func TestS3Storage_VirtualHostedURL(t *testing.T) {
	t.Parallel()
	st, _ := NewS3Storage(S3Options{
		Endpoint:        "https://s3.example.com",
		Region:          "us-east-1",
		Bucket:          "bkt",
		AccessKeyID:     "id",
		SecretAccessKey: "secret",
		UsePathStyle:    false,
	})
	u := st.urlForKey("k")
	if u.Host != "bkt.s3.example.com" {
		t.Fatalf("vhost url host: %q", u.Host)
	}
	if u.Path != "/k" {
		t.Fatalf("vhost url path: %q", u.Path)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// SigV4 primitives
// ──────────────────────────────────────────────────────────────────────────────

// Verifies the documented HMAC chain from
// https://docs.aws.amazon.com/general/latest/gr/sigv4-calculate-signature.html
// using the worked example from that page.
func TestDeriveSigningKey_AWSWorkedExample(t *testing.T) {
	t.Parallel()
	got := deriveSigningKey(
		"wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		"20150830",
		"us-east-1",
		"iam",
	)
	const want = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	gotHex := ""
	for _, b := range got {
		const hexdig = "0123456789abcdef"
		gotHex += string(hexdig[b>>4]) + string(hexdig[b&0xF])
	}
	if gotHex != want {
		t.Fatalf("deriveSigningKey: got %s want %s", gotHex, want)
	}
}

func TestAWSURIEncode(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"abc":           "abc",
		"a b":           "a%20b",
		"a/b":           "a%2Fb", // encodeSlash=true
		"~_-.":          "~_-.",
		"unicode:αβ":    "unicode%3A%CE%B1%CE%B2",
		"plus+and=eq":   "plus%2Band%3Deq",
	}
	for in, want := range cases {
		if got := awsURIEncode(in, true); got != want {
			t.Errorf("awsURIEncode(%q): got %q want %q", in, got, want)
		}
	}
	if got := awsURIEncode("a/b", false); got != "a/b" {
		t.Errorf("awsURIEncode keepSlash: got %q", got)
	}
}
