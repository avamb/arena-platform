// Package seatlayout is the one transactional operation that changes a
// session's seats: repaint them into another category, close them, open
// them, and revert any of that (task S1c, spec
// 08_architecture/26_seat_layout_management_plan_ru.md section 3.3).
//
// ApplyBatch runs in ONE transaction with the platform lock order
// (AGENTS.md): the sessions row first (IncrementSessionSeatStatusVersion,
// the only seat_status_version bump of the batch), then the named
// session_seats rows FOR UPDATE in seat_key order — the order
// CreateSeatedHold locks them in, so a batch and a buyer's hold serialize
// on the sessions row and can never deadlock — then one conditional UPDATE,
// then the session_seat_batches row. inventory_ledger is not touched: a
// batch changes neither the number of places nor what is held or sold. The
// whole transaction is retried on 40P01 / 40001 (txretry).
//
// Rules (owner decision 2026-10-15, re-layout after sales started):
//
//   - a sold or held seat is frozen: the batch skips it with a reason and
//     goes on (Strict turns any skip into a *ConflictError, for the
//     per-seat category PATCH, which has always been all-or-nothing);
//   - a General Admission place opens and closes but never changes
//     category (ReasonGAUnit);
//   - every batch is recorded with the from/to state of each changed seat,
//     and a revert is a NEW batch with the inverse changes, under the same
//     rules, linked both ways (reverts_batch_id / reverted_by_batch_id).
package seatlayout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// Kind of a batch, mirrored by the CHECK of session_seat_batches.kind
// (migration 0162; TestKinds_MatchMigrationCheckConstraint keeps the pair).
type Kind string

// Batch kinds.
const (
	KindPaint       Kind = "paint"
	KindClose       Kind = "close"
	KindOpen        Kind = "open"
	KindApplyLayout Kind = "apply_layout"
	KindRevert      Kind = "revert"
)

// Kinds lists every batch kind in the order of the migration's CHECK.
var Kinds = []Kind{KindPaint, KindClose, KindOpen, KindApplyLayout, KindRevert}

// The two statuses a batch may set.
const (
	StatusAvailable   = "available"
	StatusUnavailable = "unavailable"
)

// MaxChanges bounds one batch, the cap the category PATCH always had.
const MaxChanges = 10_000

// TxStarter is the narrow subset of a pool a batch needs.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Change is one seat of a batch request. TierID nil keeps the category,
// Status nil keeps the status; at least one of them must be set.
type Change struct {
	SeatKey string     `json:"seat_key"`
	TierID  *uuid.UUID `json:"tier_id,omitempty"`
	Status  *string    `json:"status,omitempty"`
}

// NewTier is a category to create inside the batch (spec 26 section 3.3).
// Not supported yet: a non-empty list is refused with ErrNewTiersUnsupported.
type NewTier struct {
	Name        string `json:"name"`
	PriceAmount int64  `json:"price_amount"`
}

// Actor is who applies a batch. ID is NULL for a principal that is not a
// UUID; Label is the actor type ("user", "service", ...).
type Actor struct {
	ID    *uuid.UUID
	Label string
}

// AuditFunc writes the caller's audit event inside the batch transaction,
// after the batch row exists (so it can carry batch_id).
type AuditFunc func(ctx context.Context, tx pgx.Tx, b Batch) error

// BatchInput is one ApplyBatch request.
type BatchInput struct {
	// OrgID is the organization the caller acts in. Zero means "the
	// session's own" (the flat category route resolves the organization in
	// its shim); a non-zero value that is not the session's organization is
	// ErrSessionNotFound.
	OrgID     uuid.UUID
	SessionID uuid.UUID
	Actor     Actor
	Kind      Kind
	Changes   []Change
	NewTiers  []NewTier
	// Strict makes any skipped seat a *ConflictError (nothing is written).
	Strict bool
	Audit  AuditFunc
}

// AppliedChange is one changed seat, as stored in session_seat_batches.changes.
type AppliedChange struct {
	SeatKey string    `json:"seat_key"`
	From    SeatState `json:"from"`
	To      SeatState `json:"to"`
}

// SkippedSeat is one seat the batch left alone, as stored in
// session_seat_batches.skipped. Status is the seat's status when it was
// skipped (empty for an unknown key).
type SkippedSeat struct {
	SeatKey string `json:"seat_key"`
	Reason  string `json:"reason"`
	Status  string `json:"status,omitempty"`
}

// NoopSeat is a seat that already was in the requested state. Not stored.
type NoopSeat struct {
	SeatKey string
	Status  string
}

// Batch is one recorded batch.
type Batch struct {
	ID                uuid.UUID
	OrgID             uuid.UUID
	SessionID         uuid.UUID
	ActorID           *uuid.UUID
	ActorLabel        string
	Kind              Kind
	SeatStatusVersion int64
	AppliedAt         time.Time
	RevertedAt        *time.Time
	RevertedByBatchID *uuid.UUID
	RevertsBatchID    *uuid.UUID
	Changes           []AppliedChange
	Skipped           []SkippedSeat
	// Noops is filled only on the Batch ApplyBatch / Revert return.
	Noops []NoopSeat
}

// Errors.
var (
	ErrSessionNotFound     = errors.New("seatlayout: session not found")
	ErrNewTiersUnsupported = errors.New("seatlayout: creating a category inside a batch is not supported")
	ErrBatchNotFound       = errors.New("seatlayout: batch not found")
	ErrAlreadyReverted     = errors.New("seatlayout: batch already reverted")
	ErrRevertOfRevert      = errors.New("seatlayout: a revert batch cannot be reverted")
)

// InputError is a request the batch refuses before touching the database.
type InputError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *InputError) Error() string { return "seatlayout: " + e.Message }

// TierError names categories that are not live categories of the session.
type TierError struct{ TierIDs []uuid.UUID }

func (e *TierError) Error() string {
	return fmt.Sprintf("seatlayout: %d tier(s) are not categories of this session", len(e.TierIDs))
}

// ConflictError is a Strict batch that would have skipped seats.
type ConflictError struct{ Skipped []SkippedSeat }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("seatlayout: %d seat(s) cannot be changed", len(e.Skipped))
}

// NothingToRevertError is a revert in which no seat can be put back (every
// seat is sold, held, changed again by a later batch, or its category is
// gone). Nothing is written and the original batch stays revertable.
type NothingToRevertError struct{ Skipped []SkippedSeat }

func (e *NothingToRevertError) Error() string {
	return fmt.Sprintf("seatlayout: nothing to revert, %d seat(s) skipped", len(e.Skipped))
}

// AuditError wraps a failure of the caller's AuditFunc.
type AuditError struct{ Err error }

func (e *AuditError) Error() string { return "seatlayout: audit: " + e.Err.Error() }
func (e *AuditError) Unwrap() error { return e.Err }

// Validate checks a request's shape without the database.
func Validate(in BatchInput) error {
	if len(in.NewTiers) > 0 {
		return ErrNewTiersUnsupported
	}
	switch in.Kind {
	case KindPaint, KindClose, KindOpen, KindApplyLayout:
	default:
		return &InputError{Code: "seat_batch.invalid_kind",
			Message: "kind must be one of paint|close|open|apply_layout",
			Details: map[string]any{"field": "kind"}}
	}
	if len(in.Changes) == 0 {
		return &InputError{Code: "seat_batch.no_changes", Message: "changes must be a non-empty array",
			Details: map[string]any{"field": "changes"}}
	}
	if len(in.Changes) > MaxChanges {
		return &InputError{Code: "seat_batch.too_many_changes", Message: "changes exceeds the per-batch cap",
			Details: map[string]any{"max": MaxChanges, "received": len(in.Changes)}}
	}
	seen := make(map[string]Change, len(in.Changes))
	for i, c := range in.Changes {
		bad := func(msg string) error {
			return &InputError{Code: "seat_batch.invalid_change", Message: msg,
				Details: map[string]any{"index": i, "seat_key": c.SeatKey}}
		}
		if c.SeatKey == "" {
			return bad("changes[].seat_key is required")
		}
		if c.TierID == nil && c.Status == nil {
			return bad("a change needs tier_id or status")
		}
		if c.Status != nil && *c.Status != StatusAvailable && *c.Status != StatusUnavailable {
			return bad("status must be available or unavailable")
		}
		switch in.Kind {
		case KindPaint:
			if c.TierID == nil || c.Status != nil {
				return bad("a paint batch sets tier_id only")
			}
		case KindClose:
			if c.TierID != nil || c.Status == nil || *c.Status != StatusUnavailable {
				return bad("a close batch sets status unavailable only")
			}
		case KindOpen:
			if c.TierID != nil || c.Status == nil || *c.Status != StatusAvailable {
				return bad("an open batch sets status available only")
			}
		}
		if prev, dup := seen[c.SeatKey]; dup && !sameChange(prev, c) {
			return &InputError{Code: "seat_batch.duplicate_seat_key",
				Message: "a seat_key appears twice with different targets",
				Details: map[string]any{"seat_key": c.SeatKey}}
		}
		seen[c.SeatKey] = c
	}
	return nil
}

func sameChange(a, b Change) bool {
	if (a.TierID == nil) != (b.TierID == nil) || (a.TierID != nil && *a.TierID != *b.TierID) {
		return false
	}
	return (a.Status == nil) == (b.Status == nil) && (a.Status == nil || *a.Status == *b.Status)
}

// ApplyBatch validates in and applies it in one retried transaction.
func ApplyBatch(ctx context.Context, pool TxStarter, q *gen.Queries, in BatchInput) (Batch, error) {
	if pool == nil || q == nil {
		return Batch{}, errors.New("seatlayout: ApplyBatch requires a pool and queries")
	}
	if err := Validate(in); err != nil {
		return Batch{}, err
	}
	var out Batch
	err := inTx(ctx, pool, q, func(tx pgx.Tx, txq *gen.Queries) error {
		b, err := ApplyBatchTx(ctx, tx, txq, in)
		out = b
		return err
	})
	if err != nil {
		return Batch{}, err
	}
	return out, nil
}

// ApplyBatchTx applies a validated batch inside the caller's transaction.
// Its first statement is the sessions row lock, so the caller must not have
// touched this session's seats or ledger earlier in tx. No retry here: the
// caller owns the transaction.
func ApplyBatchTx(ctx context.Context, tx pgx.Tx, txq *gen.Queries, in BatchInput) (Batch, error) {
	if err := Validate(in); err != nil {
		return Batch{}, err
	}
	version, orgID, err := lockSession(ctx, txq, in.SessionID, in.OrgID)
	if err != nil {
		return Batch{}, err
	}
	return applyChangesLocked(ctx, tx, txq, version, orgID, in)
}

// applyChangesLocked is ApplyBatchTx after the sessions row lock: check the
// categories, lock the seats, decide, write. ApplyLayout calls it too, with
// the changes it built under the same lock.
func applyChangesLocked(ctx context.Context, tx pgx.Tx, txq *gen.Queries, version int64, orgID uuid.UUID, in BatchInput) (Batch, error) {
	tierIDs := distinctTierIDs(in.Changes)
	if len(tierIDs) > 0 {
		live, err := txq.ListActiveSessionTierIDs(ctx, in.SessionID, tierIDs)
		if err != nil {
			return Batch{}, fmt.Errorf("seatlayout: load categories: %w", err)
		}
		if missing := missingIDs(tierIDs, live); len(missing) > 0 {
			return Batch{}, &TierError{TierIDs: missing}
		}
	}

	targets := make([]Target, 0, len(in.Changes))
	seen := make(map[string]bool, len(in.Changes))
	for _, c := range in.Changes {
		if seen[c.SeatKey] {
			continue
		}
		seen[c.SeatKey] = true
		targets = append(targets, Target{SeatKey: c.SeatKey, SetTier: c.TierID != nil, TierID: c.TierID, Status: c.Status})
	}
	return applyLocked(ctx, tx, txq, applyArgs{
		orgID: orgID, sessionID: in.SessionID, version: version, actor: in.Actor,
		kind: in.Kind, targets: targets, strict: in.Strict, audit: in.Audit,
	})
}

// RevertInput is one Revert request.
type RevertInput struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	BatchID   uuid.UUID
	Actor     Actor
	Audit     AuditFunc
}

// Revert applies a NEW batch (kind revert) that puts every seat the
// original batch changed back into its "from" state. A seat is put back
// only while it is still exactly as the original left it, and the sold /
// held rule applies again; every other seat goes to skipped. A batch is
// reverted at most once, and a revert batch is never reverted. When not a
// single seat can be put back the revert is refused with
// *NothingToRevertError and writes nothing (no bump, no batch row), so the
// original stays revertable once the later batch is reverted.
func Revert(ctx context.Context, pool TxStarter, q *gen.Queries, in RevertInput) (Batch, error) {
	if pool == nil || q == nil {
		return Batch{}, errors.New("seatlayout: Revert requires a pool and queries")
	}
	var out Batch
	err := inTx(ctx, pool, q, func(tx pgx.Tx, txq *gen.Queries) error {
		b, err := revertTx(ctx, tx, txq, in)
		out = b
		return err
	})
	if err != nil {
		return Batch{}, err
	}
	return out, nil
}

func revertTx(ctx context.Context, tx pgx.Tx, txq *gen.Queries, in RevertInput) (Batch, error) {
	version, orgID, err := lockSession(ctx, txq, in.SessionID, in.OrgID)
	if err != nil {
		return Batch{}, err
	}
	row, err := txq.GetSessionSeatBatchForUpdate(ctx, in.BatchID, in.SessionID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Batch{}, ErrBatchNotFound
		}
		return Batch{}, fmt.Errorf("seatlayout: load batch: %w", err)
	}
	if Kind(row.Kind) == KindRevert {
		return Batch{}, ErrRevertOfRevert
	}
	if row.RevertedAt != nil {
		return Batch{}, ErrAlreadyReverted
	}
	original, err := batchFromRow(row)
	if err != nil {
		return Batch{}, err
	}

	targets := make([]Target, 0, len(original.Changes))
	var tierIDs []uuid.UUID
	for _, c := range original.Changes {
		expect := c.To
		status := c.From.Status
		targets = append(targets, Target{
			SeatKey: c.SeatKey, SetTier: true, TierID: c.From.TierID, Status: &status, Expect: &expect,
		})
		if c.From.TierID != nil {
			tierIDs = append(tierIDs, *c.From.TierID)
		}
	}
	live := map[uuid.UUID]bool{}
	if len(tierIDs) > 0 {
		ids, err := txq.ListActiveSessionTierIDs(ctx, in.SessionID, uniqueIDs(tierIDs))
		if err != nil {
			return Batch{}, fmt.Errorf("seatlayout: load categories: %w", err)
		}
		for _, id := range ids {
			live[id] = true
		}
	}
	b, err := applyLocked(ctx, tx, txq, applyArgs{
		orgID: orgID, sessionID: in.SessionID, version: version, actor: in.Actor,
		kind: KindRevert, targets: targets, revertsBatchID: &original.ID,
		tierOK: func(id uuid.UUID) bool { return live[id] },
		audit:  in.Audit,
	})
	if err != nil {
		return Batch{}, err
	}
	return b, nil
}

// lockSession takes the sessions row lock (the batch's one version bump)
// and resolves the session's organization.
func lockSession(ctx context.Context, txq *gen.Queries, sessionID, wantOrg uuid.UUID) (int64, uuid.UUID, error) {
	version, err := txq.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, uuid.Nil, ErrSessionNotFound
		}
		return 0, uuid.Nil, fmt.Errorf("seatlayout: bump seat_status_version: %w", err)
	}
	orgRow, err := txq.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, uuid.Nil, ErrSessionNotFound
		}
		return 0, uuid.Nil, fmt.Errorf("seatlayout: session organization: %w", err)
	}
	if wantOrg != uuid.Nil && orgRow.OrgID != wantOrg {
		return 0, uuid.Nil, ErrSessionNotFound
	}
	return version, orgRow.OrgID, nil
}

type applyArgs struct {
	orgID, sessionID uuid.UUID
	version          int64
	actor            Actor
	kind             Kind
	targets          []Target
	strict           bool
	revertsBatchID   *uuid.UUID
	tierOK           func(uuid.UUID) bool
	audit            AuditFunc
}

// applyLocked runs after the sessions lock: lock the seats, decide, write.
func applyLocked(ctx context.Context, tx pgx.Tx, txq *gen.Queries, a applyArgs) (Batch, error) {
	sort.Slice(a.targets, func(i, j int) bool { return a.targets[i].SeatKey < a.targets[j].SeatKey })
	keys := make([]string, 0, len(a.targets))
	for _, t := range a.targets {
		keys = append(keys, t.SeatKey)
	}
	byKey := map[string]*gen.SessionSeatRow{}
	if len(keys) > 0 {
		rows, err := txq.LockSessionSeatsForBatch(ctx, a.sessionID, keys)
		if err != nil {
			return Batch{}, fmt.Errorf("seatlayout: lock seats: %w", err)
		}
		for i := range rows {
			byKey[rows[i].SeatKey] = &rows[i]
		}
	}

	changes := make([]AppliedChange, 0, len(a.targets))
	skipped := make([]SkippedSeat, 0)
	noops := make([]NoopSeat, 0)
	ids := make([]string, 0, len(a.targets))
	tiers := make([]*string, 0, len(a.targets))
	statuses := make([]string, 0, len(a.targets))
	for _, t := range a.targets {
		row := byKey[t.SeatKey]
		r := Decide(row, t, a.tierOK)
		switch r.Outcome {
		case OutcomeApplied:
			changes = append(changes, AppliedChange{SeatKey: r.SeatKey, From: r.From, To: r.To})
			ids = append(ids, row.ID.String())
			var tier *string
			if r.To.TierID != nil {
				s := r.To.TierID.String()
				tier = &s
			}
			tiers = append(tiers, tier)
			statuses = append(statuses, r.To.Status)
		case OutcomeNoop:
			noops = append(noops, NoopSeat{SeatKey: r.SeatKey, Status: r.From.Status})
		default:
			skipped = append(skipped, SkippedSeat{SeatKey: r.SeatKey, Reason: r.Reason, Status: r.From.Status})
		}
	}
	if a.strict && len(skipped) > 0 {
		return Batch{}, &ConflictError{Skipped: skipped}
	}
	if a.revertsBatchID != nil && len(ids) == 0 {
		// A revert that would put nothing back writes nothing: the
		// original stays revertable, so reverting the later batch that
		// moved its seats first and then this one still works.
		return Batch{}, &NothingToRevertError{Skipped: skipped}
	}
	if len(ids) > 0 {
		n, err := txq.ApplySessionSeatBatchUpdates(ctx, a.sessionID, a.version, ids, tiers, statuses)
		if err != nil {
			return Batch{}, fmt.Errorf("seatlayout: update seats: %w", err)
		}
		if n != int64(len(ids)) {
			// Unreachable while the rows are locked: the decision and the
			// UPDATE read the same state. Fail rather than record a batch
			// that claims a change that did not happen.
			return Batch{}, fmt.Errorf("seatlayout: updated %d of %d locked seats", n, len(ids))
		}
	}

	changesJSON, err := json.Marshal(changes)
	if err != nil {
		return Batch{}, fmt.Errorf("seatlayout: encode changes: %w", err)
	}
	skippedJSON, err := json.Marshal(skipped)
	if err != nil {
		return Batch{}, fmt.Errorf("seatlayout: encode skipped: %w", err)
	}
	row, err := txq.InsertSessionSeatBatch(ctx, gen.InsertSessionSeatBatchParams{
		OrgID: a.orgID, SessionID: a.sessionID, ActorID: a.actor.ID, ActorLabel: clip(a.actor.Label, 200),
		Kind: string(a.kind), SeatStatusVersion: a.version, RevertsBatchID: a.revertsBatchID,
		Changes: changesJSON, Skipped: skippedJSON,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("seatlayout: record batch: %w", err)
	}
	if a.revertsBatchID != nil {
		n, err := txq.MarkSessionSeatBatchReverted(ctx, *a.revertsBatchID, row.ID, row.AppliedAt)
		if err != nil {
			return Batch{}, fmt.Errorf("seatlayout: mark reverted: %w", err)
		}
		if n != 1 {
			return Batch{}, ErrAlreadyReverted
		}
	}
	b := Batch{
		ID: row.ID, OrgID: row.OrgID, SessionID: row.SessionID, ActorID: row.ActorID,
		ActorLabel: row.ActorLabel, Kind: Kind(row.Kind), SeatStatusVersion: row.SeatStatusVersion,
		AppliedAt: row.AppliedAt, RevertsBatchID: row.RevertsBatchID,
		Changes: changes, Skipped: skipped, Noops: noops,
	}
	if a.audit != nil {
		if err := a.audit(ctx, tx, b); err != nil {
			return Batch{}, &AuditError{Err: err}
		}
	}
	return b, nil
}

// inTx runs fn in a fresh transaction, commits, and retries the whole
// transaction on a deadlock or serialization failure.
func inTx(ctx context.Context, pool TxStarter, q *gen.Queries, fn func(tx pgx.Tx, txq *gen.Queries) error) error {
	return txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("seatlayout: begin tx: %w", err)
		}
		// WithoutCancel: the rollback must still run when ctx is cancelled.
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if err := fn(tx, q.WithTx(tx)); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("seatlayout: commit: %w", err)
		}
		return nil
	})
}

// BatchFromRow decodes a stored batch.
func BatchFromRow(row gen.SessionSeatBatchRow) (Batch, error) { return batchFromRow(row) }

func batchFromRow(row gen.SessionSeatBatchRow) (Batch, error) {
	b := Batch{
		ID: row.ID, OrgID: row.OrgID, SessionID: row.SessionID, ActorID: row.ActorID,
		ActorLabel: row.ActorLabel, Kind: Kind(row.Kind), SeatStatusVersion: row.SeatStatusVersion,
		AppliedAt: row.AppliedAt, RevertedAt: row.RevertedAt, RevertedByBatchID: row.RevertedByBatchID,
		RevertsBatchID: row.RevertsBatchID, Changes: []AppliedChange{}, Skipped: []SkippedSeat{},
	}
	if len(row.Changes) > 0 {
		if err := json.Unmarshal(row.Changes, &b.Changes); err != nil {
			return Batch{}, fmt.Errorf("seatlayout: decode changes of %s: %w", row.ID, err)
		}
	}
	if len(row.Skipped) > 0 {
		if err := json.Unmarshal(row.Skipped, &b.Skipped); err != nil {
			return Batch{}, fmt.Errorf("seatlayout: decode skipped of %s: %w", row.ID, err)
		}
	}
	return b, nil
}

// SeatProbe is the jsonb containment value that finds one seat's entries
// in changes or skipped: `[{"seat_key": key}]`.
func SeatProbe(seatKey string) []byte {
	b, _ := json.Marshal([]map[string]string{{"seat_key": seatKey}})
	return b
}

func distinctTierIDs(changes []Change) []uuid.UUID {
	var ids []uuid.UUID
	for _, c := range changes {
		if c.TierID != nil {
			ids = append(ids, *c.TierID)
		}
	}
	return uniqueIDs(ids)
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func missingIDs(want, have []uuid.UUID) []uuid.UUID {
	got := make(map[uuid.UUID]bool, len(have))
	for _, id := range have {
		got[id] = true
	}
	var out []uuid.UUID
	for _, id := range want {
		if !got[id] {
			out = append(out, id)
		}
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
