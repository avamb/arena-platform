package emailstatus

import (
	"bytes"
	"encoding/json"
	"errors"
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

// MaxBatch caps the events one webhook delivery may carry (Brevo can batch
// events into a JSON array when batching is switched on).
const MaxBatch = 100

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
	// Payload is the minimal raw record kept with the event: the provider's
	// own event name and timestamp. Never the recipient address.
	Payload map[string]any
}

// ParseBrevo decodes a Brevo transactional webhook body: one event object or
// an array of at most MaxBatch of them. now stands in for an event without a
// usable timestamp.
func ParseBrevo(body []byte, now time.Time) ([]ProviderEvent, error) {
	body = bytes.TrimSpace(body)
	var objs []map[string]any
	switch {
	case len(body) > 0 && body[0] == '[':
		if err := decodeNumbers(body, &objs); err != nil {
			return nil, ErrMalformed
		}
		if len(objs) > MaxBatch {
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
	if class == ClassFailure {
		ev.Reason = truncate(opsalert.ScrubEmails(strings.TrimSpace(str(o["reason"]))), maxReasonRunes)
	}
	return ev
}

// brevoTime prefers ts_epoch (true UTC, milliseconds). Brevo's ts / ts_event
// are seconds; "date" is a local time string of the account and is used
// only as a last resort. An absent, absurd or future time becomes now.
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
		} else if p, err := time.Parse("2006-01-02 15:04:05", d); err == nil { // allow:timeformat
			t = p
		}
	}
	if t.IsZero() || t.Year() < 2020 || t.After(now.Add(time.Hour)) {
		return now.UTC()
	}
	return t.UTC()
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
