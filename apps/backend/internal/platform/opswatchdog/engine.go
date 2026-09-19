package opswatchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// AlertRow is the durable state of one ops_alerts row (or its in-memory
// equivalent in tests). Details carries structured context (order id,
// amount, currency, counts — never PII) rendered into the Telegram message.
type AlertRow struct {
	Fingerprint    string
	Severity       string
	Title          string
	Details        map[string]any
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
	LastNotifiedAt *time.Time
	ResolvedAt     *time.Time
}

// AlertInput is what a check supplies to AlertEngine.Raise/Sync for one
// currently-observed problem.
type AlertInput struct {
	Fingerprint string
	Severity    string // one of: info, warn, high, critical
	Title       string
	Details     map[string]any
}

// AlertStore persists AlertRow state. PGAlertStore is the production
// implementation (ops_alerts table); tests use an in-memory fake.
type AlertStore interface {
	// Get returns the row for fingerprint regardless of resolved state, or
	// nil if the fingerprint has never been seen.
	Get(ctx context.Context, fingerprint string) (*AlertRow, error)
	// Upsert inserts or updates the row for row.Fingerprint and clears
	// resolved_at (the fingerprint is, by definition, open again).
	Upsert(ctx context.Context, row AlertRow) error
	// MarkResolved sets resolved_at=at for fingerprint if it is currently
	// open. A no-op (not an error) if the fingerprint is absent or already
	// resolved.
	MarkResolved(ctx context.Context, fingerprint string, at time.Time) error
	// ListOpenFingerprints returns every fingerprint with resolved_at IS
	// NULL whose fingerprint starts with prefix+":".
	ListOpenFingerprints(ctx context.Context, prefix string) ([]string, error)
}

// AlertEngine implements the dedup/re-notify/resolve lifecycle described in
// the watchdog spec: a fingerprint is notified once, re-notified every
// RenotifyInterval while it keeps recurring, and resolved (with a distinct
// "resolved" message) once a run no longer reports it.
//
// The engine is pure with respect to time and I/O — it only ever calls
// Store and Notifier — so it is fully unit-testable with a fake store and a
// clock.FakeClock, without a database or a real Telegram endpoint.
type AlertEngine struct {
	Store            AlertStore
	Notifier         opsalert.Notifier
	Clock            clock.Clock
	RenotifyInterval time.Duration
	Logger           *slog.Logger
}

// DefaultRenotifyInterval is how often an open alert whose condition keeps
// recurring is re-sent to Telegram.
const DefaultRenotifyInterval = 30 * time.Minute

func (e *AlertEngine) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

func (e *AlertEngine) renotifyInterval() time.Duration {
	if e.RenotifyInterval > 0 {
		return e.RenotifyInterval
	}
	return DefaultRenotifyInterval
}

// Raise records that in.Fingerprint's condition is observed as of now.
// It notifies on first sight (or when a previously-resolved fingerprint
// recurs) and again every renotifyInterval while it stays open; every call
// updates last_seen_at/details regardless of whether it notifies.
func (e *AlertEngine) Raise(ctx context.Context, in AlertInput) error {
	now := e.Clock.Now()

	existing, err := e.Store.Get(ctx, in.Fingerprint)
	if err != nil {
		return fmt.Errorf("opswatchdog: get alert %s: %w", in.Fingerprint, err)
	}

	isNew := existing == nil || existing.ResolvedAt != nil

	row := AlertRow{
		Fingerprint: in.Fingerprint,
		Severity:    in.Severity,
		Title:       in.Title,
		Details:     in.Details,
		LastSeenAt:  now,
	}
	if isNew {
		row.FirstSeenAt = now
	} else {
		row.FirstSeenAt = existing.FirstSeenAt
	}

	shouldNotify := isNew
	if !isNew {
		if existing.LastNotifiedAt == nil || now.Sub(*existing.LastNotifiedAt) >= e.renotifyInterval() {
			shouldNotify = true
		}
	}

	if shouldNotify {
		t := now
		row.LastNotifiedAt = &t
	} else {
		row.LastNotifiedAt = existing.LastNotifiedAt
	}

	if err := e.Store.Upsert(ctx, row); err != nil {
		return fmt.Errorf("opswatchdog: upsert alert %s: %w", in.Fingerprint, err)
	}

	if !shouldNotify {
		return nil
	}

	text := formatAlertMessage(in.Severity, in.Title, in.Details, isNew)
	if err := e.Notifier.Send(ctx, text); err != nil {
		e.logger().Warn("opswatchdog: notify failed", "fingerprint", in.Fingerprint, "error", err.Error())
	}
	return nil
}

// Resolve marks fingerprint resolved (if it is currently open) and sends a
// "resolved" message. It is a no-op when the fingerprint is absent or
// already resolved — Resolve must be idempotent since a check calls it on
// every run for every fingerprint that no longer reproduces.
func (e *AlertEngine) Resolve(ctx context.Context, fingerprint, title string) error {
	existing, err := e.Store.Get(ctx, fingerprint)
	if err != nil {
		return fmt.Errorf("opswatchdog: get alert %s: %w", fingerprint, err)
	}
	if existing == nil || existing.ResolvedAt != nil {
		return nil
	}

	now := e.Clock.Now()
	if err := e.Store.MarkResolved(ctx, fingerprint, now); err != nil {
		return fmt.Errorf("opswatchdog: resolve alert %s: %w", fingerprint, err)
	}

	if err := e.Notifier.Send(ctx, formatResolvedMessage(title)); err != nil {
		e.logger().Warn("opswatchdog: resolved-notify failed", "fingerprint", fingerprint, "error", err.Error())
	}
	return nil
}

// Sync reconciles the full set of currently-observed problems under
// fingerprintPrefix: every entry in current is Raise()d, and every
// currently-open fingerprint under the prefix that is NOT in current is
// Resolve()d. titleForResolve maps a fingerprint back to a human title for
// the resolved message (falls back to the fingerprint itself when nil).
func (e *AlertEngine) Sync(ctx context.Context, fingerprintPrefix string, current []AlertInput, titleForResolve func(fingerprint string) string) error {
	seen := make(map[string]bool, len(current))
	for _, in := range current {
		seen[in.Fingerprint] = true
		if err := e.Raise(ctx, in); err != nil {
			return err
		}
	}

	open, err := e.Store.ListOpenFingerprints(ctx, fingerprintPrefix)
	if err != nil {
		return fmt.Errorf("opswatchdog: list open alerts for %s: %w", fingerprintPrefix, err)
	}
	sort.Strings(open) // deterministic order for tests/logs
	for _, fp := range open {
		if seen[fp] {
			continue
		}
		title := fp
		if titleForResolve != nil {
			if t := titleForResolve(fp); t != "" {
				title = t
			}
		}
		if err := e.Resolve(ctx, fp, title); err != nil {
			return err
		}
	}
	return nil
}

// severityEmoji maps a severity string to a leading emoji for the Telegram
// message.
func severityEmoji(severity string) string {
	switch severity {
	case "critical":
		return "🚨"
	case "high":
		return "🔶"
	case "warn":
		return "⚠️"
	default:
		return "ℹ️"
	}
}

// formatAlertMessage renders a Raise()d alert as HTML-safe Telegram text.
func formatAlertMessage(severity, title string, details map[string]any, isNew bool) string {
	var b strings.Builder
	b.WriteString(severityEmoji(severity))
	b.WriteString(" <b>")
	b.WriteString(opsalert.EscapeHTML(strings.ToUpper(severity)))
	b.WriteString("</b> ")
	b.WriteString(opsalert.EscapeHTML(title))
	if !isNew {
		b.WriteString(" (still open)")
	}
	writeDetails(&b, details)
	return b.String()
}

// formatResolvedMessage renders a Resolve()d alert as HTML-safe Telegram
// text.
func formatResolvedMessage(title string) string {
	return "✅ resolved: <b>" + opsalert.EscapeHTML(title) + "</b>"
}

// writeDetails appends a sorted "key: value" line per detail entry so the
// message is deterministic (stable ordering makes tests and side-by-side
// diffing of repeated alerts easy).
func writeDetails(b *strings.Builder, details map[string]any) {
	if len(details) == 0 {
		return
	}
	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\n")
		b.WriteString(opsalert.EscapeHTML(k))
		b.WriteString(": ")
		b.WriteString(opsalert.EscapeHTML(fmt.Sprintf("%v", details[k])))
	}
}
