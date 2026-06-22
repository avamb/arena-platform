// Package i18n provides locale negotiation, message translation, and
// context-based localizer helpers for the arena_new platform.
//
// This file owns the go-i18n/v2 Bundle wrapper that loads TOML message
// catalogs from the embedded locales/ directory.  The Bundle is
// constructed once at startup and shared across requests; individual
// per-request Localizers are derived from it via LocalizerFor.
//
// Supported locales for this milestone: en (default), ru.
// The locales/ directory is structured to trivially accept uk and es
// catalogs in subsequent milestones — add a new .toml file and the
// Bundle picks it up automatically via the embed.FS glob.
package i18n

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.toml
var localesFS embed.FS

// Bundle wraps a go-i18n/v2 Bundle, providing locale negotiation and
// message lookup for the arena_new platform.
//
// The zero value is not usable; always call NewBundle.
type Bundle struct {
	b *goi18n.Bundle
}

// NewBundle constructs a Bundle by loading all *.toml message catalogs
// from the embedded locales/ directory.  Returns an error if any catalog
// fails to parse — this is typically a startup-fatal condition.
//
// The default language is English; missing messages in other catalogs
// fall back to the English value automatically via go-i18n's built-in
// fallback chain.
func NewBundle() (*Bundle, error) {
	b := goi18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	err := fs.WalkDir(localesFS, "locales", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".toml") {
			return nil
		}

		data, readErr := localesFS.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("i18n: read %q: %w", path, readErr)
		}
		// Use only the filename (not the full path) so go-i18n can extract
		// the language tag from the base name (e.g. "en.toml" → English).
		name := filepath.Base(path)
		if _, parseErr := b.ParseMessageFileBytes(data, name); parseErr != nil {
			return fmt.Errorf("i18n: parse %q: %w", name, parseErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &Bundle{b: b}, nil
}

// LocalizerFor creates a go-i18n Localizer for the supplied locale tag
// (e.g. "ru", "en"). The localizer falls back to the bundle's default
// language (English) for any message not present in the requested locale.
func (b *Bundle) LocalizerFor(locale string) *goi18n.Localizer {
	return goi18n.NewLocalizer(b.b, locale)
}
