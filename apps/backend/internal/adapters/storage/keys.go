// Key validation rules shared by every adapter.
//
// Keys are the backend-relative locators used to address a stored object.
// They are constrained to a small safe subset so neither the filesystem
// adapter (which would otherwise allow path traversal) nor the S3 adapter
// (which would otherwise allow absolute or oddly-encoded keys) can be
// tricked into reaching outside the configured root / bucket.

package storage

import (
	"fmt"
	"strings"
)

// validateKey returns nil when key is safe to use as a storage locator,
// or an error wrapping ErrInvalidKey otherwise.
//
// Rules:
//   - non-empty
//   - no leading "/"
//   - no trailing "/"
//   - no NUL byte
//   - no empty path segment ("//")
//   - no "." or ".." segments (defeats both fs traversal and odd S3 keys)
//   - no backslashes (Windows path separators would silently bypass the
//     forward-slash splitter on POSIX-style backends; reject them so the
//     two backends behave identically on every host OS).
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: key is empty", ErrInvalidKey)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: key %q must not start with '/'", ErrInvalidKey, key)
	}
	if strings.HasSuffix(key, "/") {
		return fmt.Errorf("%w: key %q must not end with '/'", ErrInvalidKey, key)
	}
	if strings.ContainsRune(key, 0) {
		return fmt.Errorf("%w: key contains NUL byte", ErrInvalidKey)
	}
	if strings.Contains(key, "\\") {
		return fmt.Errorf("%w: key %q contains backslash", ErrInvalidKey, key)
	}
	for _, seg := range strings.Split(key, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: key %q contains empty segment", ErrInvalidKey, key)
		case ".", "..":
			return fmt.Errorf("%w: key %q contains %q segment", ErrInvalidKey, key, seg)
		}
	}
	return nil
}
