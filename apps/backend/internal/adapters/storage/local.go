// Filesystem-backed Storage implementation.
//
// Each object is stored as two files under the configured root:
//
//	<root>/<key>          — raw object bytes
//	<root>/<key>.meta     — text file with one "key: value" line per metadata
//	                        field. Currently captures Content-Type so Get
//	                        round-trips the MIME type set at Put time.
//
// Writes are atomic at the per-file level: bytes are first streamed to a
// sibling temp file in the same directory and then renamed into place. A
// crash mid-write leaves the temp file behind and never exposes a partial
// object to readers.
//
// Concurrency: safe for use from multiple goroutines. Per-key ordering is
// not guaranteed; concurrent Put calls for the same key race, with the last
// rename winning (matches S3 semantics).

package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalStorage stores objects on the local filesystem under a root directory.
type LocalStorage struct {
	root string
}

// NewLocalStorage constructs a LocalStorage rooted at root. The directory is
// created (mode 0o755) if it does not yet exist. Returns an error when root
// is empty or cannot be created.
func NewLocalStorage(root string) (*LocalStorage, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("storage/local: root directory is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage/local: resolve absolute root %q: %w", root, err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("storage/local: create root %q: %w", absRoot, err)
	}
	return &LocalStorage{root: absRoot}, nil
}

// Backend reports BackendLocal.
func (s *LocalStorage) Backend() Backend { return BackendLocal }

// pathFor maps a validated storage key to its absolute on-disk path.
// validateKey rules guarantee the resulting path stays inside s.root.
func (s *LocalStorage) pathFor(key string) string {
	// Use filepath.FromSlash so on Windows the forward slashes are converted
	// to backslashes — the key validator rejects literal backslashes so the
	// result is always a single deterministic path per key.
	return filepath.Join(s.root, filepath.FromSlash(key))
}

// Put writes the object atomically. Any pre-existing object at the same key
// is replaced.
func (s *LocalStorage) Put(_ context.Context, in PutInput) (Object, error) {
	if err := validateKey(in.Key); err != nil {
		return Object{}, err
	}
	if in.Body == nil {
		return Object{}, errors.New("storage/local: PutInput.Body is required")
	}

	dst := s.pathFor(in.Key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Object{}, fmt.Errorf("storage/local: create parent dir for %q: %w", in.Key, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*.tmp")
	if err != nil {
		return Object{}, fmt.Errorf("storage/local: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails.
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	n, copyErr := io.Copy(tmp, in.Body)
	if closeErr := tmp.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return Object{}, fmt.Errorf("storage/local: write temp file: %w", copyErr)
	}
	if in.Size > 0 && n != in.Size {
		return Object{}, fmt.Errorf("%w: declared=%d actual=%d", ErrSizeMismatch, in.Size, n)
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		return Object{}, fmt.Errorf("storage/local: rename temp to %q: %w", in.Key, err)
	}

	if err := writeMetaSidecar(dst, in.ContentType); err != nil {
		return Object{}, err
	}

	return Object{
		Key:         in.Key,
		ContentType: in.ContentType,
		Size:        n,
	}, nil
}

// Get opens the stored object for streaming.
func (s *LocalStorage) Get(_ context.Context, key string) (*GetResult, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	path := s.pathFor(key)

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("storage/local: stat %q: %w", key, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("storage/local: open %q: %w", key, err)
	}
	ct, _ := readMetaSidecar(path) // best-effort; tolerate missing sidecar.

	return &GetResult{
		Object: Object{Key: key, ContentType: ct, Size: info.Size()},
		Body:   f,
	}, nil
}

// Stat returns metadata for the object without opening it.
func (s *LocalStorage) Stat(_ context.Context, key string) (Object, error) {
	if err := validateKey(key); err != nil {
		return Object{}, err
	}
	path := s.pathFor(key)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Object{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return Object{}, fmt.Errorf("storage/local: stat %q: %w", key, err)
	}
	ct, _ := readMetaSidecar(path)
	return Object{Key: key, ContentType: ct, Size: info.Size()}, nil
}

// Delete removes the object and its metadata sidecar.
func (s *LocalStorage) Delete(_ context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	path := s.pathFor(key)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return fmt.Errorf("storage/local: remove %q: %w", key, err)
	}
	// Metadata sidecar is best-effort; ignore missing-file errors.
	if err := os.Remove(path + ".meta"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage/local: remove metadata sidecar for %q: %w", key, err)
	}
	return nil
}

// writeMetaSidecar persists the small set of metadata fields that the
// filesystem cannot natively preserve (currently just Content-Type).
func writeMetaSidecar(objectPath, contentType string) error {
	if contentType == "" {
		// Nothing to persist; skip writing the sidecar entirely.
		return nil
	}
	meta := "Content-Type: " + contentType + "\n"
	if err := os.WriteFile(objectPath+".meta", []byte(meta), 0o644); err != nil {
		return fmt.Errorf("storage/local: write metadata sidecar for %q: %w", objectPath, err)
	}
	return nil
}

// readMetaSidecar parses a sidecar file and returns the Content-Type value
// when present. Missing files return ("", nil); malformed lines are skipped.
func readMetaSidecar(objectPath string) (string, error) {
	f, err := os.Open(objectPath + ".meta")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		const prefix = "Content-Type:"
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), nil
		}
	}
	return "", sc.Err()
}

// compile-time assertion
var _ Storage = (*LocalStorage)(nil)
