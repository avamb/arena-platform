// Package layoutgen proposes a seat layout — price categories and the seat
// each one covers — for a seated session (spec 26 §4.1, task S1e of spec 40).
//
// The allocator is a REPLACEABLE MODULE (owner decision 2026-10-15): every
// algorithm implements Algorithm and registers itself once; the HTTP routes,
// the seat-manager component and the bot read the list of algorithms and the
// schema of their parameters from the registry and never name an algorithm.
// A new algorithm is one file plus a Register call in its init().
//
// The package is pure: no database, no clock, no randomness. The same
// geometry, annotations, session state and parameters always give the same
// layout byte for byte. It never applies anything and never touches a sold
// or held seat (spec 26 §4.3): those seats are "frozen", keep their category
// and their sale price, and count in the revenue estimate at the price they
// were sold or held for ("sold by fact + the rest at the new prices", owner
// decision 2026-10-15, point 5).
package layoutgen

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// MaxSlots is the most categories a layout may propose: the geometry's own
// ceiling (seating.MaxCategories, decision В-3 of 2026-10-15).
const MaxSlots = seating.MaxCategories

// Algorithm is one way of proposing a layout.
//
//   - ID is a stable machine name (`^[a-z][a-z0-9_]{0,31}$`). It is stored
//     with the layout (S1d) so the owner can see which algorithms are used.
//   - Params describes the parameters the algorithm takes. The caller
//     validates a request against it (ParamSchema.Validate) before Suggest
//     runs, so Suggest only ever sees well-typed, in-range values with the
//     defaults filled in.
//   - Suggest proposes the layout. It must assign a slot to EVERY seat of
//     the geometry that is not frozen and to no frozen seat; Run checks that
//     contract so a broken algorithm fails loudly instead of re-pricing a
//     sold seat.
type Algorithm interface {
	ID() string
	Params() ParamSchema
	Suggest(g seating.Geometry, a Annotations, st SessionState, p Values) (Layout, Metrics, error)
}

// Annotations are the plan facts an algorithm may need that are not part of
// the geometry and do not change its checksum (spec 26 §3.2:
// seating_plans.annotations). Until S1d stores them they arrive in the body
// of the suggest request.
type Annotations struct {
	// FocalPoint is the stage, in canvas coordinates.
	FocalPoint *seating.Point `json:"focal_point,omitempty"`
	// SectionWeights rates sections from 0 (least wanted) to 1 (as good
	// as the geometry says). A section that is not named weighs 1.
	SectionWeights map[string]float64 `json:"section_weights,omitempty"`
	// ObstructedSeatKeys are seats with a restricted view.
	ObstructedSeatKeys []string `json:"obstructed_seat_keys,omitempty"`
}

// Annotation names, as listed by ParamSchema.RequiredAnnotations.
const (
	AnnotationFocalPoint = "focal_point"
)

// FrozenSeat is a sold or held seat. The allocator leaves it where it is.
type FrozenSeat struct {
	// Status is "sold" or "held".
	Status string `json:"status"`
	// PriceMinor is what the seat was sold or held for, in minor units.
	PriceMinor int64 `json:"price_minor"`
}

// Frozen statuses.
const (
	StatusSold = "sold"
	StatusHeld = "held"
)

// SessionState is what the session already decided about its seats: the
// third argument the spec calls soldHeld. Closed seats are not frozen (an
// algorithm may give them a category for the day they reopen) but they sell
// nothing, so they stay out of the revenue estimate.
type SessionState struct {
	// Currency is the ISO 4217 code every price of the session is in.
	Currency string
	// SoldHeld maps a seat_key to its frozen state.
	SoldHeld map[string]FrozenSeat
	// Closed is the set of seat_keys an operator closed for sale.
	Closed map[string]bool
}

// Slot is one proposed category.
type Slot struct {
	Key         string `json:"key"`
	Name        string `json:"name,omitempty"`
	Color       string `json:"color,omitempty"`
	PriceAmount int64  `json:"price_amount"`
	// Share is the slot's target share of the hall, 0..1.
	Share float64 `json:"share"`
}

// Layout is the proposal, in the shape seat_layouts.payload will store
// (spec 26 §3.2). Assignments covers every seat that is not frozen.
type Layout struct {
	Slots       []Slot            `json:"slots"`
	Assignments map[string]string `json:"assignments"`
	// FrozenSeatKeys are the sold and held seats left untouched, sorted.
	FrozenSeatKeys []string `json:"frozen_seat_keys"`
}

// SlotMetrics is the estimate of one slot.
type SlotMetrics struct {
	SlotKey     string `json:"slot_key"`
	PriceAmount int64  `json:"price_amount"`
	// Seats is how many free seats the layout puts in the slot, closed
	// seats included; OpenSeats leaves the closed ones out.
	Seats     int `json:"seats"`
	OpenSeats int `json:"open_seats"`
	// RevenueMinor is OpenSeats x PriceAmount.
	RevenueMinor int64 `json:"revenue_minor"`
	// Share is the slot's actual share of the hall (frozen seats count in
	// the hall, not in any slot).
	Share float64 `json:"share"`
}

// Metrics is the revenue estimate of a layout: sold seats at the price they
// were sold for, held seats at the price they are held for, every open free
// seat at the proposed price. It is a reference figure, never called an
// optimum (spec 26 §4.1 step 5).
type Metrics struct {
	Currency   string `json:"currency"`
	TotalSeats int    `json:"total_seats"`
	// SoldFrozen is how many seats are frozen (sold + held).
	SoldFrozen  int `json:"sold_frozen"`
	SoldSeats   int `json:"sold_seats"`
	HeldSeats   int `json:"held_seats"`
	ClosedSeats int `json:"closed_seats"`

	RevenueSoldMinor      int64 `json:"revenue_sold_minor"`
	RevenueHeldMinor      int64 `json:"revenue_held_minor"`
	RevenueRemainingMinor int64 `json:"revenue_remaining_minor"`
	// RevenueEstimateMinor = sold + held + remaining.
	RevenueEstimateMinor int64 `json:"revenue_estimate_minor"`

	BySlot []SlotMetrics `json:"by_slot"`
}

// ─── errors ─────────────────────────────────────────────────────────────────

// ParamError is a request parameter outside the algorithm's schema.
type ParamError struct {
	Param  string
	Reason string
}

func (e *ParamError) Error() string {
	return fmt.Sprintf("layoutgen: parameter %q: %s", e.Param, e.Reason)
}

// AnnotationError is an annotation with an invalid value.
type AnnotationError struct {
	Annotation string
	Reason     string
}

func (e *AnnotationError) Error() string {
	return fmt.Sprintf("layoutgen: annotation %q: %s", e.Annotation, e.Reason)
}

// AnnotationRequiredError is an annotation the algorithm needs and did not
// get. The stage is never guessed (spec 26 §4.1 step 1): the operator taps
// it once on the plan.
type AnnotationRequiredError struct {
	Annotation string
}

func (e *AnnotationRequiredError) Error() string {
	return fmt.Sprintf("layoutgen: annotation %q is required", e.Annotation)
}

// ErrNoSeats is a geometry without a single seat (a general-admission plan).
var ErrNoSeats = errors.New("layoutgen: the plan has no seats")

// ContractError is an algorithm that broke the output contract. It is a
// programming error, never the caller's.
type ContractError struct {
	Algorithm string
	Reason    string
}

func (e *ContractError) Error() string {
	return fmt.Sprintf("layoutgen: algorithm %q broke the layout contract: %s", e.Algorithm, e.Reason)
}

// ─── registry ───────────────────────────────────────────────────────────────

var algorithmIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Registry holds algorithms by ID.
type Registry struct {
	mu   sync.RWMutex
	algs map[string]Algorithm
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{algs: map[string]Algorithm{}}
}

// Register adds a. It refuses an invalid ID, a duplicate ID and a schema
// that does not describe itself correctly.
func (r *Registry) Register(a Algorithm) error {
	if a == nil {
		return errors.New("layoutgen: nil algorithm")
	}
	id := a.ID()
	if !algorithmIDPattern.MatchString(id) {
		return fmt.Errorf("layoutgen: invalid algorithm id %q", id)
	}
	if err := a.Params().check(); err != nil {
		return fmt.Errorf("layoutgen: algorithm %q: %w", id, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.algs[id]; dup {
		return fmt.Errorf("layoutgen: algorithm %q is already registered", id)
	}
	r.algs[id] = a
	return nil
}

// Lookup returns the algorithm with that ID.
func (r *Registry) Lookup(id string) (Algorithm, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.algs[id]
	return a, ok
}

// List returns every algorithm, sorted by ID.
func (r *Registry) List() []Algorithm {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Algorithm, 0, len(r.algs))
	for _, a := range r.algs {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

var defaultRegistry = NewRegistry()

// Register adds a to the process-wide registry and panics when it cannot:
// it runs from an init(), where a bad algorithm must stop the binary.
func Register(a Algorithm) {
	if err := defaultRegistry.Register(a); err != nil {
		// allow:panic: init-time programmer error (bad or duplicate algorithm), never request-driven.
		panic(err)
	}
}

// Lookup finds an algorithm in the process-wide registry.
func Lookup(id string) (Algorithm, bool) { return defaultRegistry.Lookup(id) }

// List returns the process-wide registry's algorithms, sorted by ID.
func List() []Algorithm { return defaultRegistry.List() }
