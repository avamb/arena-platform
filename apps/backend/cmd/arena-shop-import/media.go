package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// maxImageBytes bounds one picture (a product photo is a few MB at most).
const maxImageBytes = 15 << 20

// shopOwnerType is the media_objects owner type of a shop picture
// (migration 0172, mediastore.AllowedOwnerTypes).
const shopOwnerType = "shop_product"

var allowedImageTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true,
}

// mediaStore is the part of *mediastore.Repo the importer uses.
type mediaStore interface {
	Insert(ctx context.Context, in mediastore.InsertInput) (mediastore.Object, error)
	GetByID(ctx context.Context, id uuid.UUID) (mediastore.Object, error)
	PutAndStream(ctx context.Context, key, contentType string, body io.Reader) (string, int64, error)
	Backend() string
	SetDimensionsIfMissing(ctx context.Context, id uuid.UUID, width, height int32) (bool, error)
}

// imageData is a downloaded or locally read picture.
type imageData struct {
	Body        []byte
	ContentType string
	Checksum    string
}

// imageSource reads pictures from a local folder first (--images-dir), then
// from the URL of the CSV row.
type imageSource struct {
	dir    string
	client *http.Client
}

func newImageSource(dir string) *imageSource {
	return &imageSource{dir: dir, client: &http.Client{Timeout: 60 * time.Second}}
}

// fileName is the picture's name inside a Shopify CDN URL.
func fileName(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return ""
	}
	name := path.Base(u.Path)
	if dec, err := url.PathUnescape(name); err == nil {
		name = dec
	}
	if name == "." || name == "/" || name == "" {
		return ""
	}
	return name
}

// localPath returns the local copy of src when --images-dir holds one.
func (s *imageSource) localPath(src string) (string, bool) {
	if s.dir == "" {
		return "", false
	}
	name := fileName(src)
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	// filepath.Base strips any directory part; the rel check below keeps a
	// name such as ".." from escaping the images directory.
	name = filepath.Base(name)
	if name == "." || name == ".." {
		return "", false
	}
	p := filepath.Join(s.dir, name)
	if rel, err := filepath.Rel(s.dir, p); err != nil || rel != name {
		return "", false
	}
	if st, err := os.Stat(p); err == nil && !st.IsDir() { //nolint:gosec // name is a bare file name confined to s.dir by the Rel check above
		return p, true
	}
	return "", false
}

// available reports, without touching the network, whether src can be read:
// a local copy exists, or it is an http(s) address that load will try.
func (s *imageSource) available(src string) bool {
	if _, ok := s.localPath(src); ok {
		return true
	}
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

var errNotImage = errors.New("not a jpeg, png, webp or gif picture")

// load reads the picture. The bytes decide the type, not the header.
func (s *imageSource) load(ctx context.Context, src string) (imageData, error) {
	var body []byte
	if p, ok := s.localPath(src); ok {
		b, err := os.ReadFile(p) //nolint:gosec // operator-controlled folder
		if err != nil {
			return imageData{}, err
		}
		body = b
	} else {
		if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
			return imageData{}, fmt.Errorf("no local copy and %q is not an http(s) address", src)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return imageData{}, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return imageData{}, fmt.Errorf("download: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return imageData{}, fmt.Errorf("download answered HTTP %d", resp.StatusCode)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
		if err != nil {
			return imageData{}, fmt.Errorf("download: %w", err)
		}
	}
	if len(body) == 0 {
		return imageData{}, errors.New("empty file")
	}
	if len(body) > maxImageBytes {
		return imageData{}, fmt.Errorf("larger than %d MB", maxImageBytes>>20)
	}
	ct := http.DetectContentType(body)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if !allowedImageTypes[ct] {
		return imageData{}, errNotImage
	}
	sum := sha256.Sum256(body)
	return imageData{Body: body, ContentType: ct, Checksum: hex.EncodeToString(sum[:])}, nil
}
