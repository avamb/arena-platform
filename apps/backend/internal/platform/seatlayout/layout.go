// layout.go — saved seat layouts and their application (task S1d, spec
// 08_architecture/26_seat_layout_management_plan_ru.md sections 3.2-3.3).
//
// A layout is a set of slots (categories with a name, colour and price), the
// slot of each seat and the seats to close. It is bound to a seating PLAN and
// applied to a session by seat_key. Applying goes through the same batch
// machinery as the brush: ONE transaction, the sessions row locked first
// (the batch's only seat_status_version bump), the seats locked in seat_key
// order, sold and held seats skipped with a reason, the result recorded as
// a batch of kind apply_layout that can be reverted like any other.
//
// What applying does NOT do: it never reopens a seat the layout does not
// list as closed, never touches a General Admission place's category and
// never edits an existing category (name, colour, price); a slot is mapped to
// a category of the session by id, by name, or created.
package seatlayout

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// Layout sources, mirrored by the CHECK of seat_layouts.source (migration
// 0171; TestLayoutSources_MatchMigrationCheckConstraint keeps the pair).
const (
	SourceManual            = "manual"
	SourceGeometry          = "geometry"
	SourceAllocator         = "allocator"
	SourceCopiedFromSession = "copied_from_session"
)

// LayoutSources lists the sources in the order of the CHECK.
var LayoutSources = []string{SourceManual, SourceGeometry, SourceAllocator, SourceCopiedFromSession}

// Limits of a layout.
const (
	// MaxLayoutsPerPlan caps the layouts of one organization on one plan.
	MaxLayoutsPerPlan = 100
	// MaxSlots is the platform ceiling of categories on a plan.
	MaxSlots = seating.MaxCategories
	// MaxLayoutNameLen is the length of a layout name, like the CHECK.
	MaxLayoutNameLen = 120
	maxSlotNameLen   = 100
	maxSlotKeyLen    = 40
	maxSeatKeyLen    = 200
	maxPriceMinor    = 1_000_000_000
)

var (
	slotKeyRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	colorRE    = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)
)

// LayoutSlot is one category of a layout.
type LayoutSlot struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	PriceAmount int64  `json:"price_amount"`
	// Currency is optional; when set it must be the session's currency for
	// a category to be created from this slot.
	Currency  string `json:"currency,omitempty"`
	SortOrder int32  `json:"sort_order"`
	// Share is the allocator's target share of the hall, 0..1; kept for the
	// round trip of a saved proposal, never read when applying.
	Share *float64 `json:"share,omitempty"`
}

// LayoutPayload is the stored body of a layout (seat_layouts.payload).
type LayoutPayload struct {
	Slots          []LayoutSlot      `json:"slots"`
	Assignments    map[string]string `json:"assignments"`
	ClosedSeatKeys []string          `json:"closed_seat_keys"`
	// FrozenSeatKeys is what an allocator proposal names as left alone. It
	// is accepted so a proposal can be saved as it came and dropped by
	// Normalize: a frozen seat is a fact of one session, not of a layout.
	FrozenSeatKeys []string `json:"frozen_seat_keys,omitempty"`
}

// LayoutError is a layout the platform refuses to store or apply.
type LayoutError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *LayoutError) Error() string { return "seatlayout: " + e.Message }

func layoutErr(field, msg string) *LayoutError {
	return &LayoutError{Code: "seat_layout.invalid_payload", Message: msg, Details: map[string]any{"field": field}}
}

// NormalizeLayoutName trims a layout name and checks its length.
func NormalizeLayoutName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > MaxLayoutNameLen {
		return "", &LayoutError{Code: "seat_layout.invalid_name",
			Message: fmt.Sprintf("name must be 1 to %d characters", MaxLayoutNameLen),
			Details: map[string]any{"field": "name", "max": MaxLayoutNameLen}}
	}
	return n, nil
}

// Normalize checks a payload and returns the form that is stored: slot
// colours lower-cased, closed seat keys sorted and de-duplicated, the frozen
// list dropped. It does not look at any plan or session.
func (p LayoutPayload) Normalize() (LayoutPayload, error) {
	if len(p.Slots) == 0 || len(p.Slots) > MaxSlots {
		return LayoutPayload{}, layoutErr("slots", fmt.Sprintf("a layout has 1 to %d slots", MaxSlots))
	}
	out := LayoutPayload{
		Slots:       make([]LayoutSlot, 0, len(p.Slots)),
		Assignments: make(map[string]string, len(p.Assignments)),
	}
	keys := map[string]bool{}
	names := map[string]bool{}
	for i, s := range p.Slots {
		field := fmt.Sprintf("slots[%d]", i)
		if !slotKeyRE.MatchString(s.Key) {
			return LayoutPayload{}, layoutErr(field+".key", fmt.Sprintf("slot key must match [A-Za-z0-9_-]{1,%d}", maxSlotKeyLen))
		}
		if keys[s.Key] {
			return LayoutPayload{}, layoutErr(field+".key", "slot keys must be unique")
		}
		keys[s.Key] = true
		name := strings.TrimSpace(s.Name)
		if name == "" || utf8.RuneCountInString(name) > maxSlotNameLen {
			return LayoutPayload{}, layoutErr(field+".name", fmt.Sprintf("slot name must be 1 to %d characters", maxSlotNameLen))
		}
		folded := strings.ToLower(name)
		if names[folded] {
			// Applying matches a slot to a category by name, so two slots
			// of one name could not be told apart.
			return LayoutPayload{}, layoutErr(field+".name", "slot names must be unique, ignoring case")
		}
		names[folded] = true
		color := s.Color
		if color != "" {
			if !colorRE.MatchString(color) {
				return LayoutPayload{}, layoutErr(field+".color", "colour must be #rrggbb")
			}
			color = strings.ToLower(color)
		}
		if s.PriceAmount < 0 || s.PriceAmount > maxPriceMinor {
			return LayoutPayload{}, layoutErr(field+".price_amount", "price_amount must be between 0 and 1000000000 minor units")
		}
		if s.Currency != "" && !currencyRE.MatchString(s.Currency) {
			return LayoutPayload{}, layoutErr(field+".currency", "currency must be a 3-letter upper-case ISO 4217 code")
		}
		if s.Share != nil && (*s.Share < 0 || *s.Share > 1) {
			return LayoutPayload{}, layoutErr(field+".share", "share must be between 0 and 1")
		}
		out.Slots = append(out.Slots, LayoutSlot{
			Key: s.Key, Name: name, Color: color, PriceAmount: s.PriceAmount,
			Currency: s.Currency, SortOrder: s.SortOrder, Share: s.Share,
		})
	}

	seats := map[string]bool{}
	for seat, slot := range p.Assignments {
		if seat == "" || len(seat) > maxSeatKeyLen {
			return LayoutPayload{}, layoutErr("assignments", fmt.Sprintf("a seat key is 1 to %d characters", maxSeatKeyLen))
		}
		if !keys[slot] {
			return LayoutPayload{}, &LayoutError{Code: "seat_layout.invalid_payload",
				Message: "an assignment names a slot the layout does not have",
				Details: map[string]any{"field": "assignments", "seat_key": seat, "slot": slot}}
		}
		out.Assignments[seat] = slot
		seats[seat] = true
	}
	closed := map[string]bool{}
	for _, seat := range p.ClosedSeatKeys {
		if seat == "" || len(seat) > maxSeatKeyLen {
			return LayoutPayload{}, layoutErr("closed_seat_keys", fmt.Sprintf("a seat key is 1 to %d characters", maxSeatKeyLen))
		}
		closed[seat] = true
		seats[seat] = true
	}
	out.ClosedSeatKeys = make([]string, 0, len(closed))
	for seat := range closed {
		out.ClosedSeatKeys = append(out.ClosedSeatKeys, seat)
	}
	sort.Strings(out.ClosedSeatKeys)

	if len(seats) == 0 {
		return LayoutPayload{}, layoutErr("assignments", "a layout names at least one seat")
	}
	if len(seats) > MaxChanges {
		return LayoutPayload{}, &LayoutError{Code: "seat_layout.too_many_seats",
			Message: "a layout is limited to the seats of one batch",
			Details: map[string]any{"max": MaxChanges, "received": len(seats)}}
	}
	return out, nil
}

// FromGeometry builds the layout of a plan's own categories: one slot per
// category that has seats, every seat in the slot its category index names.
// Names follow overrides (the plan's category_name_overrides, keyed by the
// category index as a string), colours are the geometry's. Prices are the
// import hints (minor units) when they parse, else 0. General Admission
// zones are not seats and are left out.
func FromGeometry(g seating.Geometry, nameOverrides map[string]string) (LayoutPayload, error) {
	used := map[int]bool{}
	assign := map[string]int{}
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, seat := range row.Seats {
				used[seat.CategoryIndex] = true
				assign[seat.Key] = seat.CategoryIndex
			}
		}
	}
	p := LayoutPayload{Assignments: make(map[string]string, len(assign))}
	slotOf := map[int]string{}
	for _, c := range g.Categories {
		if !used[c.Index] || c.IsGA() {
			continue
		}
		key := fmt.Sprintf("c%d", c.Index)
		name := strings.TrimSpace(c.Name)
		if o := strings.TrimSpace(nameOverrides[fmt.Sprint(c.Index)]); o != "" {
			name = o
		}
		if name == "" {
			name = fmt.Sprintf("Category %d", c.Index)
		}
		var price int64
		if c.PriceHint != "" {
			var v int64
			if _, err := fmt.Sscan(c.PriceHint, &v); err == nil && v > 0 && v <= maxPriceMinor {
				price = v
			}
		}
		color := ""
		if colorRE.MatchString(c.Color) {
			color = c.Color
		}
		p.Slots = append(p.Slots, LayoutSlot{Key: key, Name: name, Color: color, PriceAmount: price, SortOrder: int32(c.Index)}) //nolint:gosec // geometry indexes are small
		slotOf[c.Index] = key
	}
	for seat, idx := range assign {
		if key, ok := slotOf[idx]; ok {
			p.Assignments[seat] = key
		}
	}
	// Two geometry categories may share a name once overrides are applied;
	// keep the first and fold the other into it so the layout stays valid.
	seenName := map[string]string{}
	remap := map[string]string{}
	slots := p.Slots[:0:0]
	for _, s := range p.Slots {
		folded := strings.ToLower(s.Name)
		if keep, dup := seenName[folded]; dup {
			remap[s.Key] = keep
			continue
		}
		seenName[folded] = s.Key
		slots = append(slots, s)
	}
	p.Slots = slots
	for seat, key := range p.Assignments {
		if to, ok := remap[key]; ok {
			p.Assignments[seat] = to
		}
	}
	return p.Normalize()
}

// FromSession builds the layout a session has right now from its seats and
// categories: one slot per category that holds seats, every categorised seat
// in its slot, every closed seat in closed_seat_keys. Sold and held seats are
// listed with the category they are in: the layout is a picture of how the
// hall is divided, not of what is for sale. colors maps a category to its own
// colour (absent = none).
func FromSession(tiers []gen.TicketTierRow, colors map[uuid.UUID]string, seats []gen.SessionSeatAssignmentRow) (LayoutPayload, error) {
	byID := make(map[uuid.UUID]gen.TicketTierRow, len(tiers))
	for _, t := range tiers {
		byID[t.ID] = t
	}
	usedTier := map[uuid.UUID]bool{}
	for _, s := range seats {
		if s.TierID != nil {
			usedTier[*s.TierID] = true
		}
	}
	ordered := make([]gen.TicketTierRow, 0, len(usedTier))
	for id := range usedTier {
		if t, ok := byID[id]; ok {
			ordered = append(ordered, t)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].SortOrder != ordered[j].SortOrder {
			return ordered[i].SortOrder < ordered[j].SortOrder
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	p := LayoutPayload{Assignments: map[string]string{}}
	slotOf := map[uuid.UUID]string{}
	names := map[string]int{}
	for i, t := range ordered {
		key := fmt.Sprintf("t%d", i+1)
		name := strings.TrimSpace(t.Name)
		folded := strings.ToLower(name)
		names[folded]++
		if names[folded] > 1 {
			name = fmt.Sprintf("%s (%d)", name, names[folded])
		}
		order := int32(i) //nolint:gosec // i < number of categories of one session, small
		p.Slots = append(p.Slots, LayoutSlot{
			Key: key, Name: name, Color: colors[t.ID], PriceAmount: t.PriceAmount,
			Currency: t.Currency, SortOrder: order,
		})
		slotOf[t.ID] = key
	}
	for _, s := range seats {
		if s.TierID != nil {
			if key, ok := slotOf[*s.TierID]; ok {
				p.Assignments[s.SeatKey] = key
			}
		}
		if s.Status == StatusUnavailable {
			p.ClosedSeatKeys = append(p.ClosedSeatKeys, s.SeatKey)
		}
	}
	return p.Normalize()
}

// ─── applying ───────────────────────────────────────────────────────────────

// Slot resolution actions.
const (
	// ActionMapped: the caller named the category.
	ActionMapped = "mapped"
	// ActionExisting: a live category of the session has the slot's name.
	ActionExisting = "existing"
	// ActionCreate: a category was (or, in a dry run, would be) created.
	ActionCreate = "create"
)

// SlotTier is what one slot of the layout became in the session.
type SlotTier struct {
	SlotKey string `json:"slot_key"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	// TierID is the category. For a category a dry run creates it is an id
	// that exists only inside the rolled-back transaction: show it, never
	// keep it.
	TierID *uuid.UUID `json:"tier_id"`
}

// UnmappedSlot is a slot with no category and no permission to create one.
type UnmappedSlot struct {
	SlotKey string `json:"slot_key"`
	Name    string `json:"name"`
	// Reason is "no_match" (no live category has the name) or "ambiguous"
	// (several do).
	Reason string `json:"reason"`
}

// UnmappedSlotsError is returned when slots cannot be tied to categories.
type UnmappedSlotsError struct{ Slots []UnmappedSlot }

func (e *UnmappedSlotsError) Error() string {
	return fmt.Sprintf("seatlayout: %d slot(s) have no category", len(e.Slots))
}

// SlotCurrencyError is a slot whose price is in another currency than the
// session, so a category cannot be created from it.
type SlotCurrencyError struct {
	SlotKey, Want, Got string
}

func (e *SlotCurrencyError) Error() string {
	return fmt.Sprintf("seatlayout: slot %q is priced in %s, the session sells in %s", e.SlotKey, e.Got, e.Want)
}

// Layout application errors.
var (
	// ErrSessionHasNoPlan: the session is not bound to a seating plan.
	ErrSessionHasNoPlan = errors.New("seatlayout: the session has no seating plan")
	// ErrPlanMismatch: the layout belongs to another plan than the session's.
	ErrPlanMismatch = errors.New("seatlayout: the layout is for another seating plan")
	// ErrSlotTierNotInSession: slot_tiers names a category that is not a
	// live category of the session.
	ErrSlotTierNotInSession = errors.New("seatlayout: a mapped category is not a live category of the session")

	errDryRunDone = errors.New("seatlayout: dry run rolled back")
)

// ApplyLayoutInput is one ApplyLayout request. Payload must be a normalized
// payload (LayoutPayload.Normalize).
type ApplyLayoutInput struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	// PlanID is the plan the layout belongs to; the session must be bound to
	// a version of the same plan.
	PlanID  uuid.UUID
	Actor   Actor
	Payload LayoutPayload
	// SlotTiers maps a slot key to a live category of the session.
	SlotTiers map[string]uuid.UUID
	// CreateMissingTiers creates a category for a slot no category matches.
	CreateMissingTiers bool
	// DryRun runs everything in a transaction that is rolled back.
	DryRun bool
	// Audit runs inside the transaction after the batch row exists; never
	// in a dry run.
	Audit AuditFunc
}

// ApplyLayoutResult is the outcome of ApplyLayout. In a dry run the batch is
// what WOULD have been recorded (zero ID, nothing persisted).
type ApplyLayoutResult struct {
	Batch  Batch
	Slots  []SlotTier
	DryRun bool
}

// ApplyLayout applies a layout to a session in one retried transaction.
func ApplyLayout(ctx context.Context, pool TxStarter, q *gen.Queries, in ApplyLayoutInput) (ApplyLayoutResult, error) {
	if pool == nil || q == nil {
		return ApplyLayoutResult{}, errors.New("seatlayout: ApplyLayout requires a pool and queries")
	}
	payload, err := in.Payload.Normalize()
	if err != nil {
		return ApplyLayoutResult{}, err
	}
	in.Payload = payload
	slotKeys := make(map[string]bool, len(payload.Slots))
	for _, s := range payload.Slots {
		slotKeys[s.Key] = true
	}
	for k := range in.SlotTiers {
		if !slotKeys[k] {
			return ApplyLayoutResult{}, &LayoutError{Code: "seat_layout.unknown_slot",
				Message: "slot_tiers names a slot the layout does not have",
				Details: map[string]any{"slot": k}}
		}
	}
	var out ApplyLayoutResult
	err = inTx(ctx, pool, q, func(tx pgx.Tx, txq *gen.Queries) error {
		res, err := applyLayoutTx(ctx, tx, txq, in)
		if err != nil {
			return err
		}
		out = res
		if in.DryRun {
			return errDryRunDone
		}
		return nil
	})
	if err != nil && (!in.DryRun || !errors.Is(err, errDryRunDone)) {
		return ApplyLayoutResult{}, err
	}
	out.DryRun = in.DryRun
	if in.DryRun {
		out.Batch.ID = uuid.Nil
		out.Batch.AppliedAt = out.Batch.AppliedAt.UTC()
	}
	return out, nil
}

func applyLayoutTx(ctx context.Context, tx pgx.Tx, txq *gen.Queries, in ApplyLayoutInput) (ApplyLayoutResult, error) {
	// The sessions row first, before any read the decision depends on.
	version, orgID, err := lockSession(ctx, txq, in.SessionID, in.OrgID)
	if err != nil {
		return ApplyLayoutResult{}, err
	}
	planID, err := txq.GetSessionPlanID(ctx, in.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ApplyLayoutResult{}, ErrSessionHasNoPlan
		}
		return ApplyLayoutResult{}, fmt.Errorf("seatlayout: session plan: %w", err)
	}
	if planID != in.PlanID {
		return ApplyLayoutResult{}, ErrPlanMismatch
	}

	slots, tierOf, err := resolveSlots(ctx, txq, in)
	if err != nil {
		return ApplyLayoutResult{}, err
	}

	closed := make(map[string]bool, len(in.Payload.ClosedSeatKeys))
	for _, k := range in.Payload.ClosedSeatKeys {
		closed[k] = true
	}
	unavailable := StatusUnavailable
	seen := map[string]bool{}
	changes := make([]Change, 0, len(in.Payload.Assignments)+len(closed))
	seatKeys := make([]string, 0, len(in.Payload.Assignments)+len(closed))
	for k := range in.Payload.Assignments {
		seatKeys = append(seatKeys, k)
	}
	for k := range closed {
		if _, assigned := in.Payload.Assignments[k]; !assigned {
			seatKeys = append(seatKeys, k)
		}
	}
	sort.Strings(seatKeys)
	for _, k := range seatKeys {
		if seen[k] {
			continue
		}
		seen[k] = true
		c := Change{SeatKey: k}
		if slot, ok := in.Payload.Assignments[k]; ok {
			id := tierOf[slot] // every used slot was resolved or the call failed
			c.TierID = &id
		}
		if closed[k] {
			c.Status = &unavailable
		}
		changes = append(changes, c)
	}

	batchIn := BatchInput{
		OrgID: orgID, SessionID: in.SessionID, Actor: in.Actor, Kind: KindApplyLayout,
		Changes: changes,
	}
	if !in.DryRun {
		batchIn.Audit = in.Audit
	}
	if err := Validate(batchIn); err != nil {
		return ApplyLayoutResult{}, err
	}
	b, err := applyChangesLocked(ctx, tx, txq, version, orgID, batchIn)
	if err != nil {
		return ApplyLayoutResult{}, err
	}
	return ApplyLayoutResult{Batch: b, Slots: slots}, nil
}

// resolveSlots ties each slot to a category of the session, creating the
// missing ones when allowed. Returns the resolution per slot (payload order)
// and the slot-to-category map.
func resolveSlots(ctx context.Context, txq *gen.Queries, in ApplyLayoutInput) ([]SlotTier, map[string]uuid.UUID, error) {
	// Only the slots the layout actually uses need a category.
	used := map[string]bool{}
	for _, slot := range in.Payload.Assignments {
		used[slot] = true
	}
	live, err := txq.ListTicketTiersBySession(ctx, in.SessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("seatlayout: load categories: %w", err)
	}
	liveByID := make(map[uuid.UUID]gen.TicketTierRow, len(live))
	byName := map[string][]gen.TicketTierRow{}
	for _, t := range live {
		liveByID[t.ID] = t
		f := strings.ToLower(strings.TrimSpace(t.Name))
		byName[f] = append(byName[f], t)
	}

	var unmapped []UnmappedSlot
	var slots []SlotTier
	tierOf := map[string]uuid.UUID{}
	currency := ""
	for _, s := range in.Payload.Slots {
		if !used[s.Key] {
			continue
		}
		if id, ok := in.SlotTiers[s.Key]; ok {
			if _, isLive := liveByID[id]; !isLive {
				return nil, nil, ErrSlotTierNotInSession
			}
			idc := id
			slots = append(slots, SlotTier{SlotKey: s.Key, Name: liveByID[id].Name, Action: ActionMapped, TierID: &idc})
			tierOf[s.Key] = id
			continue
		}
		matches := byName[strings.ToLower(s.Name)]
		switch {
		case len(matches) == 1:
			id := matches[0].ID
			slots = append(slots, SlotTier{SlotKey: s.Key, Name: matches[0].Name, Action: ActionExisting, TierID: &id})
			tierOf[s.Key] = id
		case len(matches) > 1:
			unmapped = append(unmapped, UnmappedSlot{SlotKey: s.Key, Name: s.Name, Reason: "ambiguous"})
		case !in.CreateMissingTiers:
			unmapped = append(unmapped, UnmappedSlot{SlotKey: s.Key, Name: s.Name, Reason: "no_match"})
		default:
			if currency == "" {
				c, err := txq.GetSessionCurrency(ctx, in.SessionID)
				if err != nil {
					return nil, nil, fmt.Errorf("seatlayout: session currency: %w", err)
				}
				currency = c
			}
			if s.Currency != "" && s.Currency != currency {
				return nil, nil, &SlotCurrencyError{SlotKey: s.Key, Want: currency, Got: s.Currency}
			}
			mode := "free"
			if s.PriceAmount > 0 {
				mode = "fixed"
			}
			row, err := txq.InsertTicketTier(ctx, in.SessionID, s.Name, mode, s.PriceAmount, currency,
				nil, nil, nil, nil, nil, s.SortOrder)
			if err != nil {
				return nil, nil, fmt.Errorf("seatlayout: create category %q: %w", s.Name, err)
			}
			if s.Color != "" {
				color := s.Color
				if _, err := txq.SetTicketTierColor(ctx, row.ID, in.SessionID, &color); err != nil {
					return nil, nil, fmt.Errorf("seatlayout: colour category %q: %w", s.Name, err)
				}
			}
			id := row.ID
			slots = append(slots, SlotTier{SlotKey: s.Key, Name: s.Name, Action: ActionCreate, TierID: &id})
			tierOf[s.Key] = id
		}
	}
	if len(unmapped) > 0 {
		return nil, nil, &UnmappedSlotsError{Slots: unmapped}
	}
	return slots, tierOf, nil
}
