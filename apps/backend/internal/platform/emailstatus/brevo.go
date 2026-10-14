package emailstatus

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// TrackingPrefix starts the X-Mailin-custom value arena writes on every
// letter; Brevo echoes the header back on each event of the message.
const TrackingPrefix = "arena-letter:"

// TrackingID is the X-Mailin-custom value of a letter.
func TrackingID(letterID uuid.UUID) string { return TrackingPrefix + letterID.String() }

// maxReasonRunes bounds the provider's free-form failure reason.
const maxReasonRunes = 300

// ErrMalformed is a body that is neither one event object nor an array of
// them.
var ErrMalformed = errors.New("emailstatus: malformed webhook body")

// ProviderEvent is one webhook event, reduced to what arena keeps.
type ProviderEvent struct {
	// Event is the normalized name (Normalize); RawEvent what Brevo sent.
	Event    string
	RawEvent string
	Class    Class
	// MessageID is Brevo's message-id without angle brackets.
	MessageID string
	// LetterID is the letter named by the X-Mailin-custom header, when the
	// event carries arena's tracking id.
	LetterID uuid.UUID
	// At is when the provider says the event happened.
	At time.Time
	// Reason is the scrubbed, truncated failure reason (bounces, blocks).
	Reason string
	// Link is the clicked link of a click event: host and path only, no
	// query or fragment, long token-like path segments replaced by
	// "{token}" (CleanLink).
	Link string
	// Payload is the minimal raw record kept with the event: the provider's
	// own event name and timestamp. Never the recipient address.
	Payload map[string]any
}

// ParseBrevo decodes a Brevo transactional webhook body: one event object or
// an array of them (Brevo batches events when batching is on). The number of
// events is not limited here: the route caps the body size, so a large batch
// is never refused and retried for ever. now stands in for an event without a
// usable timestamp.
func ParseBrevo(body []byte, now time.Time) ([]ProviderEvent, error) {
	body = bytes.TrimSpace(body)
	var objs []map[string]any
	switch {
	case len(body) > 0 && body[0] == '[':
		if err := decodeNumbers(body, &objs); err != nil {
			return nil, ErrMalformed
		}
	case len(body) > 0 && body[0] == '{':
		var one map[string]any
		if err := decodeNumbers(body, &one); err != nil {
			return nil, ErrMalformed
		}
		objs = []map[string]any{one}
	default:
		return nil, ErrMalformed
	}
	out := make([]ProviderEvent, 0, len(objs))
	for _, o := range objs {
		if o == nil {
			return nil, ErrMalformed
		}
		out = append(out, brevoEvent(o, now))
	}
	return out, nil
}

func decodeNumbers(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return ErrMalformed
	}
	return nil
}

func brevoEvent(o map[string]any, now time.Time) ProviderEvent {
	raw := str(o["event"])
	name, class := Normalize(raw)
	ev := ProviderEvent{
		Event:     name,
		RawEvent:  raw,
		Class:     class,
		MessageID: NormalizeMessageID(firstStr(o, "message-id", "messageId", "message_id")),
		At:        brevoTime(o, now),
		Payload:   map[string]any{"event": truncate(raw, 64)},
	}
	if v, ok := o["ts_epoch"].(json.Number); ok {
		ev.Payload["ts_epoch"] = v
	}
	for k, v := range o {
		if strings.EqualFold(k, "X-Mailin-custom") {
			ev.LetterID = parseTracking(str(v))
			break
		}
	}
	if name == EventClick {
		ev.Link = CleanLink(str(o["link"]))
	}
	if class == ClassFailure {
		ev.Reason = ScrubReason(str(o["reason"]))
	}
	return ev
}

// brevoTime prefers ts_epoch (true UTC, milliseconds). Brevo's ts / ts_event
// are seconds. "date" is used only as a last resort and only when it names
// its zone (RFC 3339): Brevo writes it in the account's local time without
// a zone, and reading that as UTC could shift an event by hours and change
// which of a bounce and a delivery came last. An absent, zoneless, absurd or
// future time becomes now.
func brevoTime(o map[string]any, now time.Time) time.Time {
	var t time.Time
	if n, ok := num(o["ts_epoch"]); ok {
		t = time.UnixMilli(n)
	} else if n, ok := num(o["ts_event"]); ok {
		t = time.Unix(n, 0)
	} else if n, ok := num(o["ts"]); ok {
		t = time.Unix(n, 0)
	} else if d := str(o["date"]); d != "" {
		if p, err := time.Parse(time.RFC3339, d); err == nil {
			t = p
		}
	}
	if t.IsZero() || t.Year() < 2020 || t.After(now.Add(time.Hour)) {
		return now.UTC()
	}
	return t.UTC()
}

// tokenSegment is a path segment that looks like a secret: a long run of
// letters, digits, dashes and underscores (a checkout token, a signed id).
var tokenSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)

// dottedTokenSegment is a token made of dot-separated parts (a JWT, a
// signed value): two or three long base64url parts. A file name such as
// "programme-2026.pdf" does not match: its extension is short.
var dottedTokenSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(\.[A-Za-z0-9_-]{8,})?$`)

// CleanLink reduces a clicked URL to what dispute evidence needs: scheme,
// host and path. The query and fragment are dropped (they may carry
// tracking ids or tokens), long token-like path segments become "{token}",
// and the result is at most 300 characters. Empty for anything that is not
// an absolute http(s) URL.
func CleanLink(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	segs := strings.Split(u.EscapedPath(), "/")
	for i, seg := range segs {
		if tokenSegment.MatchString(seg) || dottedTokenSegment.MatchString(seg) {
			segs[i] = "{token}"
		}
	}
	return truncate(u.Scheme+"://"+strings.ToLower(u.Host)+strings.Join(segs, "/"), 300)
}

var (
	ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Re = regexp.MustCompile(`(?i)\b[0-9a-f]{1,4}(?::[0-9a-f]{0,4}){2,7}\b`)
	hostRe = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}\b`)
)

// ScrubReason cleans a provider's free-form failure reason before it is
// stored and shown to an organizer: e-mail addresses, IP addresses and host
// names of the receiving servers are removed, whitespace folded, the length
// bounded. What stays is the SMTP code and the wording ("550 5.1.1 user
// unknown").
func ScrubReason(s string) string {
	s = opsalert.ScrubEmails(strings.Join(strings.Fields(s), " "))
	s = ipv4Re.ReplaceAllString(s, "[ip]")
	s = ipv6Re.ReplaceAllString(s, "[ip]")
	s = hostRe.ReplaceAllString(s, "[host]")
	return truncate(s, maxReasonRunes)
}

// NormalizeMessageID strips whitespace and angle brackets: Brevo writes the
// id as "<…@smtp-relay.mailin.fr>", the SMTP reply too, storage keeps neither.
func NormalizeMessageID(v string) string {
	v = strings.Trim(strings.TrimSpace(v), "<>")
	if len(v) > 250 {
		return ""
	}
	return v
}

func parseTracking(v string) uuid.UUID {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, TrackingPrefix) {
		return uuid.Nil
	}
	id, err := uuid.Parse(strings.TrimPrefix(v, TrackingPrefix))
	if err != nil {
		return uuid.Nil
	}
	return id
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

func firstStr(o map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(o[k]); s != "" {
			return s
		}
	}
	return ""
}

func num(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil {
			return int64(f), true
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
