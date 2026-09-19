// Package opsalert sends operational alert/sale notifications to Telegram.
//
// It exists so the first real ticket sales (starting with tiny volumes and
// no ops staff) get a human-visible signal on every sale and on anything
// that looks wrong, early enough to fix by hand. The package is
// deliberately narrow: it knows how to format and deliver ONE Telegram
// message at a time. It never reads business tables itself — that is
// internal/platform/opswatchdog's job — and it never blocks a caller on a
// slow/broken Telegram endpoint for longer than a few retries.
package opsalert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// TelegramMaxMessageLength is the Bot API's hard limit (UTF-16 code units,
// but callers of this package only ever pass ASCII/Cyrillic plain text plus
// a handful of HTML tags, so counting runes is close enough to be safe with
// margin). Messages longer than this are truncated with a trailing marker.
const TelegramMaxMessageLength = 4096

// truncationMarker is appended to a message that had to be cut to fit
// TelegramMaxMessageLength.
const truncationMarker = "\n… (truncated)"

// defaultBaseURL is the production Telegram Bot API origin. Tests override
// it via WithBaseURL to point at an httptest server.
const defaultBaseURL = "https://api.telegram.org"

// defaultTimeout bounds a single HTTP attempt to sendMessage.
const defaultTimeout = 10 * time.Second

// maxSendAttempts is how many times TelegramNotifier tries to deliver one
// message before giving up and only logging the failure.
const maxSendAttempts = 3

// retryBaseDelay is the base of the exponential backoff between attempts
// (attempt 1 waits ~retryBaseDelay, attempt 2 ~2x, ...).
const retryBaseDelay = 300 * time.Millisecond

// Notifier sends one already-composed alert/sale message.
//
// Implementations must never let a delivery failure propagate as a reason
// to stop the caller's work (e.g. abandon cursor advancement in the
// watchdog): a broken or unconfigured Telegram destination degrades to
// "logged, not delivered", never to "the whole run failed". Send should
// therefore return a non-nil error only in exceptional circumstances a
// caller genuinely needs to react to (none of the implementations in this
// package do that today).
type Notifier interface {
	// Send delivers text (plain text; implementations that use a formatted
	// wire protocol such as Telegram's HTML parse mode are responsible for
	// escaping any caller-supplied HTML metacharacters themselves — see
	// EscapeHTML for building text safely).
	Send(ctx context.Context, text string) error
}

// New returns the Notifier appropriate for the given Telegram credentials.
//
// When both botToken and chatID are non-empty, it returns a
// *TelegramNotifier. When either is empty, it returns a logging no-op —
// this is the safe default for local/dev/CI environments and must never be
// treated as a startup error.
func New(botToken, chatID, envLabel string, logger *slog.Logger) Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	botToken = strings.TrimSpace(botToken)
	chatID = strings.TrimSpace(chatID)
	if botToken == "" || chatID == "" {
		logger.Info("opsalert: Telegram not configured; alerts will only be logged",
			"bot_token_set", botToken != "",
			"chat_id_set", chatID != "",
		)
		return &noopNotifier{logger: logger}
	}
	return NewTelegramNotifier(botToken, chatID, envLabel, WithLogger(logger))
}

// noopNotifier logs every message instead of delivering it. Used when
// Telegram credentials are absent (dev/test) so callers never need to
// special-case "notifications disabled".
type noopNotifier struct {
	logger *slog.Logger
}

func (n *noopNotifier) Send(_ context.Context, text string) error {
	n.logger.Info("opsalert: notify (no-op, Telegram not configured)", "text", text)
	return nil
}

// TelegramNotifier delivers messages via the Telegram Bot API's sendMessage
// method (HTML parse mode) over plain net/http.
type TelegramNotifier struct {
	botToken string
	chatID   string
	envLabel string
	baseURL  string
	client   *http.Client
	logger   *slog.Logger
}

// Option configures a TelegramNotifier constructed by NewTelegramNotifier.
type Option func(*TelegramNotifier)

// WithBaseURL overrides the Telegram Bot API origin. Tests point this at an
// httptest.Server; production leaves it unset (defaultBaseURL).
func WithBaseURL(u string) Option {
	return func(n *TelegramNotifier) {
		if u != "" {
			n.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithHTTPClient overrides the *http.Client used for delivery (tests only;
// production always gets one built with defaultTimeout).
func WithHTTPClient(c *http.Client) Option {
	return func(n *TelegramNotifier) {
		if c != nil {
			n.client = c
		}
	}
}

// WithLogger overrides the logger. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(n *TelegramNotifier) {
		if l != nil {
			n.logger = l
		}
	}
}

// NewTelegramNotifier constructs a TelegramNotifier. botToken and chatID
// must both be non-empty — use New (or noopNotifier directly) when they are
// not.
func NewTelegramNotifier(botToken, chatID, envLabel string, opts ...Option) *TelegramNotifier {
	n := &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		envLabel: envLabel,
		baseURL:  defaultBaseURL,
		client:   &http.Client{Timeout: defaultTimeout},
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// sendMessageRequest is the Telegram Bot API sendMessage request body.
type sendMessageRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

// Send delivers text to the configured chat, prefixed with the environment
// label, using Telegram's HTML parse mode. text is expected to already be
// HTML-safe (see EscapeHTML) — TelegramNotifier does not re-escape it,
// since a caller building a message with bold/emphasis markup needs its own
// tags to survive.
//
// On a transport or non-2xx-response failure, Send retries up to
// maxSendAttempts times with exponential backoff. If every attempt fails,
// the failure is logged at Error level and Send still returns nil: a
// Telegram outage must never be treated as a reason to stop the caller's
// (read-only, best-effort) work.
func (n *TelegramNotifier) Send(ctx context.Context, text string) error {
	full := text
	if n.envLabel != "" {
		full = fmt.Sprintf("<b>[%s]</b> %s", EscapeHTML(n.envLabel), text)
	}
	full = truncate(full, TelegramMaxMessageLength)

	body, err := json.Marshal(sendMessageRequest{
		ChatID:                n.chatID,
		Text:                  full,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	})
	if err != nil {
		// Marshalling a struct of plain strings cannot realistically fail;
		// log and bail rather than panic.
		n.logger.Error("opsalert: failed to marshal Telegram request", "error", err.Error())
		return nil
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", n.baseURL, n.botToken)

	var lastErr error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if attempt > 1 {
			delay := retryBaseDelay * time.Duration(1<<uint(attempt-2))
			select {
			case <-ctx.Done():
				n.logger.Warn("opsalert: send aborted (context done)", "error", ctx.Err())
				return nil
			case <-time.After(delay):
			}
		}

		lastErr = n.attemptSend(ctx, url, body)
		if lastErr == nil {
			return nil
		}
		n.logger.Warn("opsalert: Telegram send attempt failed",
			"attempt", attempt,
			"max_attempts", maxSendAttempts,
			"error", lastErr.Error(),
		)
	}

	// Never log n.botToken — only the fact that delivery failed and why.
	n.logger.Error("opsalert: Telegram send failed after retries; message dropped",
		"attempts", maxSendAttempts,
		"error", lastErr.Error(),
	)
	return nil
}

// attemptSend performs a single HTTP POST to the Telegram sendMessage
// endpoint and reports a non-nil error for any transport failure or
// non-2xx response.
func (n *TelegramNotifier) attemptSend(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram responded with status %d", resp.StatusCode)
	}
	return nil
}

// truncate cuts s to at most max runes, appending truncationMarker when it
// had to cut (the marker itself counts toward max so the result never
// exceeds the limit).
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	markerRunes := []rune(truncationMarker)
	cut := max - len(markerRunes)
	if cut < 0 {
		cut = 0
	}
	return string(runes[:cut]) + truncationMarker
}

// EscapeHTML escapes the handful of characters Telegram's HTML parse mode
// treats specially (&, <, >) so caller-supplied dynamic values (event
// titles, organization names) can never break the message's markup or be
// interpreted as an (unsupported, and therefore rejected) tag.
func EscapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(s)
}

// compile-time interface guards
var (
	_ Notifier = (*TelegramNotifier)(nil)
	_ Notifier = (*noopNotifier)(nil)
)
