package salelinks

import (
	"crypto/rand"
	"crypto/sha256"
	"strconv"
	"sync"
	"time"
)

const (
	defaultDedupTTL = 24 * time.Hour
	defaultDedupMax = 100_000
)

type dedupEntry struct {
	key [sha256.Size]byte
	at  time.Time
}

// Dedup remembers which scans it has already seen, in the memory of one
// process (spec 43 section 2, decision 9): the key is
// sha256(code|ip|user agent|day|salt). Nothing personal is kept, only the hash,
// and the salt is drawn at construction and never configurable, so a key cannot
// be recomputed from outside. A restart forgets everything and counts again:
// the numbers are a guide for the organizer, not proof for an ad account.
type Dedup struct {
	mu    sync.Mutex
	salt  [32]byte
	ttl   time.Duration
	max   int
	seen  map[[sha256.Size]byte]time.Time
	queue []dedupEntry // insertion order, oldest first
	head  int
}

// NewDedup builds a Dedup. A non-positive ttl means 24 hours and a non-positive
// maxEntries means 100000. When full, the oldest record is evicted.
func NewDedup(ttl time.Duration, maxEntries int) *Dedup {
	if ttl <= 0 {
		ttl = defaultDedupTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultDedupMax
	}
	d := &Dedup{ttl: ttl, max: maxEntries, seen: make(map[[sha256.Size]byte]time.Time)}
	// Since Go 1.24 crypto/rand.Read never returns an error: if the system
	// source fails the runtime stops the program, so there is no zero-salt case.
	_, _ = rand.Read(d.salt[:])
	return d
}

func (d *Dedup) key(code, ip, userAgent string, now time.Time) [sha256.Size]byte {
	h := sha256.New()
	// Every field is length-prefixed, so a separator character inside the user
	// agent cannot shift the boundary between two fields.
	field := func(b []byte) {
		h.Write([]byte(strconv.Itoa(len(b)) + ":"))
		h.Write(b)
	}
	field([]byte(code))
	field([]byte(ip))
	field([]byte(userAgent))
	field([]byte(now.UTC().Format("2006-01-02"))) // allow:timeformat: a calendar day inside a hash, not a timestamp
	field(d.salt[:])
	var k [sha256.Size]byte
	copy(k[:], h.Sum(nil))
	return k
}

// Seen reports whether this client (ip and user agent) already scanned this
// code on this UTC day within the TTL. The first call records it and answers
// false; later calls answer true. It is safe for concurrent use.
func (d *Dedup) Seen(code, ip, userAgent string, now time.Time) bool {
	k := d.key(code, ip, userAgent, now)
	d.mu.Lock()
	defer d.mu.Unlock()

	d.purge(now)
	if at, ok := d.seen[k]; ok && now.Sub(at) < d.ttl {
		return true
	}
	if len(d.seen) >= d.max {
		d.evictOldest()
	}
	d.seen[k] = now
	d.queue = append(d.queue, dedupEntry{key: k, at: now})
	return false
}

// purge drops records older than the TTL from the front of the queue.
func (d *Dedup) purge(now time.Time) {
	for d.head < len(d.queue) {
		e := d.queue[d.head]
		if now.Sub(e.at) < d.ttl {
			break
		}
		d.drop(e)
	}
	d.compact()
}

// evictOldest removes the oldest live record to make room.
func (d *Dedup) evictOldest() {
	for d.head < len(d.queue) {
		e := d.queue[d.head]
		live := d.drop(e)
		if live {
			break
		}
	}
	d.compact()
}

// drop removes the queue head. It deletes the map record only when it is the
// one this queue entry wrote (a refreshed key has a newer entry further on) and
// reports whether it did.
func (d *Dedup) drop(e dedupEntry) bool {
	d.head++
	if at, ok := d.seen[e.key]; ok && at.Equal(e.at) {
		delete(d.seen, e.key)
		return true
	}
	return false
}

func (d *Dedup) compact() {
	if d.head > 1024 && d.head*2 > len(d.queue) {
		d.queue = append([]dedupEntry(nil), d.queue[d.head:]...)
		d.head = 0
	}
}
