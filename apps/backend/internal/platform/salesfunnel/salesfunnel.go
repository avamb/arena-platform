// Package salesfunnel holds the pure rules of the buyer sales funnel (spec 50,
// stage A7): the three client steps a widget or a page may report, the three
// surfaces they come from, the order of all six steps the organizer reads, and
// the in-memory duplicate filter of the public beacon
// (POST /v1/public/funnel).
//
// The beacon is public and unauthenticated, so the rules are deliberately
// narrow and match the hosted page's statistics beacon (package pagestats,
// whose CleanUTM, suspect-agent rule, calendar day and per-day pair cap are
// reused as they are): a stored row carries only the event, the day, the step,
// the surface, the session and the two cleaned utm values. The visitor's IP
// address and User-Agent are read to decide, never stored and never logged;
// the duplicate filter keeps only a salted hash of them, in memory, until the
// end of the day. The numbers are an organizer's guide, not proof for an ad
// account.
package salesfunnel

import (
	"crypto/rand"
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

// The client steps the beacon accepts. They are also the values of the table
// CHECK in migration 0189.
const (
	StepView        = "view"
	StepSessionOpen = "session_open"
	StepSeatsChosen = "seats_chosen"
)

// The server-side steps the read adds. They are never written by the beacon.
const (
	StepCheckoutStart = "checkout_start"
	StepPaymentOpen   = "payment_open"
	StepPaid          = "paid"
)

// The surfaces a beacon may come from (the table CHECK in migration 0189).
const (
	SurfaceWidget      = "widget"
	SurfaceHostedPage  = "hosted_page"
	SurfaceTicketsPage = "tickets_page"
)

// ClientSteps are the steps the beacon counts, in funnel order.
var ClientSteps = []string{StepView, StepSessionOpen, StepSeatsChosen}

// Steps are all six steps of the funnel an organizer reads, in order.
var Steps = []string{StepView, StepSessionOpen, StepSeatsChosen, StepCheckoutStart, StepPaymentOpen, StepPaid}

// Surfaces are the accepted beacon surfaces.
var Surfaces = []string{SurfaceWidget, SurfaceHostedPage, SurfaceTicketsPage}

const (
	// MaxBodyBytes is the largest request body the beacon reads. The body
	// holds a feed token or two slugs, two UUIDs, two utm values and two
	// short words.
	MaxBodyBytes = 2048
	// MaxTokenLength bounds a feed token as a rate-limit key.
	MaxTokenLength = 200
	// MaxSlugLength bounds a page slug.
	MaxSlugLength = 120
	// MaxDedupeEntries bounds the in-memory duplicate filter. Past it the
	// filter is emptied and starts over: a flood can only make duplicates
	// count again, never grow memory.
	MaxDedupeEntries = 200_000
)

// ParseStep returns step when it is one of the client steps the beacon counts.
func ParseStep(step string) (string, bool) {
	for _, s := range ClientSteps {
		if step == s {
			return s, true
		}
	}
	return "", false
}

// ParseSurface returns surface when it is an accepted beacon surface.
func ParseSurface(surface string) (string, bool) {
	for _, s := range Surfaces {
		if surface == s {
			return s, true
		}
	}
	return "", false
}

// LimitKey is the key of the rate-limit bucket of one funnel context: the feed
// token or the page's slug pair, lower-cased and cut so a caller cannot grow
// the bucket map with long keys.
func LimitKey(feedToken, slug, eventSlug string) string {
	if feedToken != "" {
		return "funnel/t/" + cut(feedToken, MaxTokenLength)
	}
	return "funnel/p/" + cut(strings.ToLower(slug), MaxSlugLength) + "/" + cut(strings.ToLower(eventSlug), MaxSlugLength)
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Deduper drops a beacon the same visitor already sent today. It is the same
// idea as the page-view counters of pagestats: a reload, a re-render or a
// double fire must not count twice. The visitor is never identified: the key is
// a SHA-256 over a random salt that lives only in this process and changes
// every UTC day, the client address, the User-Agent and what is being counted.
// Nothing is written anywhere, and a restart or a second replica only lets a
// duplicate count once more.
type Deduper struct {
	mu   sync.Mutex
	now  func() time.Time
	day  string
	salt [16]byte
	seen map[[16]byte]struct{}
}

// NewDeduper returns an empty filter reading the real clock. The zero Deduper
// is ready to use as well.
func NewDeduper() *Deduper { return &Deduper{} }

func (d *Deduper) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// SeenToday reports whether the same visitor already sent the same count today
// and, when not, remembers it. parts are the things being counted (event,
// session, step, surface, utm pair); ip and userAgent identify the visitor and
// are only ever hashed. The first call of a day returns false.
func (d *Deduper) SeenToday(ip, userAgent string, parts ...string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rotateLocked()
	key := d.keyLocked(ip, userAgent, parts)
	if _, ok := d.seen[key]; ok {
		return true
	}
	if len(d.seen) >= MaxDedupeEntries {
		d.seen = make(map[[16]byte]struct{})
	}
	d.seen[key] = struct{}{}
	return false
}

// Forget undoes a SeenToday whose count could not be written, so the visitor's
// next attempt is counted.
func (d *Deduper) Forget(ip, userAgent string, parts ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rotateLocked()
	delete(d.seen, d.keyLocked(ip, userAgent, parts))
}

// rotateLocked starts a new day: new salt, empty set.
func (d *Deduper) rotateLocked() {
	day := d.clock().UTC().Format("2006-01-02") // allow:timeformat: a calendar date, not a timestamp
	if d.seen != nil && day == d.day {
		return
	}
	d.day = day
	d.seen = make(map[[16]byte]struct{})
	// crypto/rand does not fail on the supported platforms; a zero salt would
	// only make the key guessable for one day, never write anything.
	_, _ = rand.Read(d.salt[:])
}

func (d *Deduper) keyLocked(ip, userAgent string, parts []string) [16]byte {
	h := sha256.New()
	h.Write(d.salt[:])
	h.Write([]byte(ip))
	h.Write([]byte{0})
	h.Write([]byte(userAgent))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	var sum [32]byte
	h.Sum(sum[:0])
	var key [16]byte
	copy(key[:], sum[:16])
	return key
}
