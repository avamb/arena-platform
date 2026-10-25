// Package attribution cleans and merges the "attribution" object that the
// browser of a buyer (or a selling site) hands to checkout: which sales link,
// if any, brought the buyer, and the utm labels of that visit (spec 43
// section 3).
//
// The package is pure: no database, no clock of its own. The same object lives
// in checkout_sessions.attribution, orders.attribution and the gateway
// CREATE_ORDER_EXT, so every surface cleans it through Clean and never by hand.
// A bad or missing object is never an error for the caller: a purchase must
// not fail because of a label (Clean answers ok=false and the order carries an
// empty object).
package attribution

import (
	"encoding/json"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
)

const (
	// Window is the last-touch window (owner decision 2026-10-24): a touch
	// older than this is not attributed. The server checks it, never the client.
	Window = 7 * 24 * time.Hour
	// FutureSkew is how far ahead of the server clock a landed_at may be
	// before the touch is dropped (clock drift of a phone).
	FutureSkew = 5 * time.Minute
)

// Touch is one visit that came through a sales link or a utm-tagged address.
type Touch struct {
	Ref         string    // the sales-link code, lower-case, may be empty
	UTMSource   string    // utm_source
	UTMMedium   string    // utm_medium
	UTMCampaign string    // utm_campaign
	UTMContent  string    // utm_content
	UTMTerm     string    // utm_term
	LandedAt    time.Time // when the buyer arrived, UTC, whole seconds
}

// First is the first touch of the buyer, kept beside the last one when the two
// differ.
type First struct {
	Ref         string
	UTMSource   string
	UTMCampaign string // additive to spec 43 section 3: a campaign-only touch is a touch
	LandedAt    time.Time
}

// Attribution is the object of spec 43 section 3: the last touch, flattened,
// and optionally the first one.
type Attribution struct {
	Touch
	First *First
}

type wireTouch struct {
	Ref         string     `json:"ref,omitempty"`
	UTMSource   string     `json:"utm_source,omitempty"`
	UTMMedium   string     `json:"utm_medium,omitempty"`
	UTMCampaign string     `json:"utm_campaign,omitempty"`
	UTMContent  string     `json:"utm_content,omitempty"`
	UTMTerm     string     `json:"utm_term,omitempty"`
	LandedAt    *time.Time `json:"landed_at,omitempty"`
}

type wireFirst struct {
	Ref         string     `json:"ref,omitempty"`
	UTMSource   string     `json:"utm_source,omitempty"`
	UTMCampaign string     `json:"utm_campaign,omitempty"`
	LandedAt    *time.Time `json:"landed_at,omitempty"`
}

type wireAttribution struct {
	wireTouch
	First *wireFirst `json:"first,omitempty"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC().Truncate(time.Second)
	return &u
}

func (t Touch) wire() wireTouch {
	return wireTouch{
		Ref: t.Ref, UTMSource: t.UTMSource, UTMMedium: t.UTMMedium,
		UTMCampaign: t.UTMCampaign, UTMContent: t.UTMContent, UTMTerm: t.UTMTerm,
		LandedAt: timePtr(t.LandedAt),
	}
}

func (f First) wire() wireFirst {
	return wireFirst{Ref: f.Ref, UTMSource: f.UTMSource, UTMCampaign: f.UTMCampaign, LandedAt: timePtr(f.LandedAt)}
}

// MarshalJSON writes landed_at as RFC 3339 UTC and only when it is set.
func (t Touch) MarshalJSON() ([]byte, error) { return json.Marshal(t.wire()) }

// MarshalJSON writes landed_at as RFC 3339 UTC and only when it is set.
func (f First) MarshalJSON() ([]byte, error) { return json.Marshal(f.wire()) }

// MarshalJSON writes the object of spec 43 section 3. It is defined on
// Attribution because the MarshalJSON of Touch would otherwise be promoted and
// drop First.
func (a Attribution) MarshalJSON() ([]byte, error) {
	w := wireAttribution{wireTouch: a.wire()}
	if a.First != nil {
		f := a.First.wire()
		w.First = &f
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads the STORED object back (a column of checkout_sessions or
// orders). Client input goes through Clean, which validates; this does not.
func (a *Attribution) UnmarshalJSON(b []byte) error {
	var w wireAttribution
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*a = Attribution{Touch: Touch{
		Ref: w.Ref, UTMSource: w.UTMSource, UTMMedium: w.UTMMedium,
		UTMCampaign: w.UTMCampaign, UTMContent: w.UTMContent, UTMTerm: w.UTMTerm,
	}}
	if w.LandedAt != nil {
		a.LandedAt = *w.LandedAt
	}
	if w.First != nil {
		f := &First{Ref: w.First.Ref, UTMSource: w.First.UTMSource, UTMCampaign: w.First.UTMCampaign}
		if w.First.LandedAt != nil {
			f.LandedAt = *w.First.LandedAt
		}
		a.First = f
	}
	return nil
}

// IsZero reports whether a carries no information: no ref, no utm label and
// no first touch.
func (a Attribution) IsZero() bool {
	return a.Ref == "" && a.UTMSource == "" && a.UTMMedium == "" &&
		a.UTMCampaign == "" && a.UTMContent == "" && a.UTMTerm == "" && a.First == nil
}

// JSON returns the compact stored form: "{}" for a zero value, which is what
// the NOT NULL jsonb columns hold when there is no attribution.
func (a Attribution) JSON() []byte {
	if a.IsZero() {
		return []byte("{}")
	}
	b, err := json.Marshal(a)
	if err != nil {
		// Unreachable: only strings and times are marshalled.
		return []byte("{}")
	}
	return b
}

// hasTouch reports whether a names a source of the visit: the same test Clean
// applies (ref, utm_source or utm_campaign).
func (a Attribution) hasTouch() bool {
	return a.Ref != "" || a.UTMSource != "" || a.UTMCampaign != ""
}

func (a Attribution) firstOfTouch() *First {
	return &First{Ref: a.Ref, UTMSource: a.UTMSource, UTMCampaign: a.UTMCampaign, LandedAt: a.LandedAt}
}

// MergeTouch returns the attribution to store when a buyer who already carries
// prev now brings next (a later touch, last touch wins, spec 43 decision 2).
// The result is next. When prev names a different source (ref, utm_source or utm_campaign),
// the first touch is kept in First: prev.First if prev already had one,
// otherwise the own touch of prev. When the source did not change, the First
// of prev is kept. When prev names no source at all, next is returned as it is.
func MergeTouch(prev, next Attribution) Attribution {
	out := next
	switch {
	case !prev.hasTouch():
		// nothing earlier to remember
	case prev.Ref != next.Ref || prev.UTMSource != next.UTMSource || prev.UTMCampaign != next.UTMCampaign:
		if prev.First != nil {
			f := *prev.First
			out.First = &f
		} else {
			out.First = prev.firstOfTouch()
		}
	default:
		if prev.First != nil {
			f := *prev.First
			out.First = &f
		}
	}
	return out
}

// Clean parses the attribution object a client sent and returns what may be
// stored. ok is false (and the Attribution zero) when nothing usable remains.
//
// Rules (spec 43 section 3):
//   - the input must be a JSON object. Unknown keys are ignored and a value of
//     the wrong type is treated as absent;
//   - every utm value goes through pagestats.CleanUTM (at most 64 characters
//     from [A-Za-z0-9._~-]; a value with any other character is dropped
//     whole);
//   - ref is folded with NormalizeRef and dropped when it is not a code;
//   - landed_at must be RFC 3339, not older than now-Window and not later than
//     now+FutureSkew, otherwise the WHOLE touch is dropped: the window is
//     checked by the server, not the client. A landed_at in the near future is
//     clamped to now. An ABSENT landed_at is accepted and set to now: a client
//     that cannot keep time must not cost a sale. A PRESENT landed_at that is
//     not a non-empty RFC 3339 string drops the touch;
//   - a touch with no ref and no utm_source/utm_campaign is not a touch;
//   - "first" is cleaned the same way (ref, utm_source, utm_campaign, landed_at)
//     except that the 7-day Window does NOT apply to it: the window belongs to
//     the last touch only, and the first touch is older than the last by
//     definition (decision R-QL23, MergeTouch carries it forward without any
//     window). Its landed_at must still be RFC 3339, not later than
//     now+FutureSkew and not later than the landed_at of the last touch;
//     otherwise "first" is dropped alone.
func Clean(raw []byte, now time.Time) (Attribution, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return Attribution{}, false
	}
	now = now.UTC()
	t, ok := cleanTouch(m, now, true)
	if !ok || (t.Ref == "" && t.UTMSource == "" && t.UTMCampaign == "") {
		return Attribution{}, false
	}
	out := Attribution{Touch: t}
	if rawFirst, has := m["first"]; has {
		var fm map[string]json.RawMessage
		if err := json.Unmarshal(rawFirst, &fm); err == nil && len(fm) > 0 {
			// The first touch is exempt from the window, and it cannot be
			// later than the last touch: then it is not the first.
			if ft, ok := cleanTouch(fm, now, false); ok && !ft.LandedAt.After(t.LandedAt) &&
				(ft.Ref != "" || ft.UTMSource != "" || ft.UTMCampaign != "") {
				out.First = &First{Ref: ft.Ref, UTMSource: ft.UTMSource, UTMCampaign: ft.UTMCampaign, LandedAt: ft.LandedAt}
			}
		}
	}
	return out, true
}

// cleanTouch cleans one touch object. windowed applies the last-touch Window
// to landed_at; it is false for the first touch, which may be older.
func cleanTouch(m map[string]json.RawMessage, now time.Time, windowed bool) (Touch, bool) {
	t := Touch{
		UTMSource:   pagestats.CleanUTM(str(m["utm_source"])),
		UTMMedium:   pagestats.CleanUTM(str(m["utm_medium"])),
		UTMCampaign: pagestats.CleanUTM(str(m["utm_campaign"])),
		UTMContent:  pagestats.CleanUTM(str(m["utm_content"])),
		UTMTerm:     pagestats.CleanUTM(str(m["utm_term"])),
	}
	if ref, ok := NormalizeRef(str(m["ref"])); ok {
		t.Ref = ref
	}
	landed := now
	// Only an ABSENT landed_at means "unknown, use now". A key that is present
	// but is not a non-empty RFC 3339 string (number, null, "", true) is a
	// broken client and drops the touch.
	if rawLanded, present := m["landed_at"]; present {
		var s string
		if err := json.Unmarshal(rawLanded, &s); err != nil || s == "" {
			return Touch{}, false
		}
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return Touch{}, false
		}
		landed = parsed.UTC()
	}
	if (windowed && landed.Before(now.Add(-Window))) || landed.After(now.Add(FutureSkew)) {
		return Touch{}, false
	}
	if landed.After(now) {
		landed = now
	}
	t.LandedAt = landed.Truncate(time.Second)
	return t, true
}

// str reads a JSON string leniently: anything that is not a string is "".
func str(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
