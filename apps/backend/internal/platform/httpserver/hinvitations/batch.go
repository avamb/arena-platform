package hinvitations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/invitations"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// batchRequestIndex is the unique index a concurrent repeat of the same
// batch request_id loses on.
const batchRequestIndex = "invitation_batches_org_request_uq"

// BatchInput is one resolved CSV upload (spec 46, section 3.5). Guests come
// from ResolveBatch with no issues, so every place is named and the guest
// data is checked; the transaction re-checks everything that can change
// (seat status, free places, capacity) under the locks.
type BatchInput struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	RequestID uuid.UUID
	// FileSHA256 is the digest of what the upload asked for (RequestDigest:
	// the file and the batch delivery), stored as invitation_batches.file_sha256.
	FileSHA256 string
	// Rows is the number of data rows of the file.
	Rows   int
	Guests []BatchGuest
	// IssuedBy is the user who uploads; nil for an organization API key.
	IssuedBy *uuid.UUID
	// Actor is the order_events actor ('user:<uuid>' or 'system').
	Actor string
	// Audit writes the v1.invitation.batch_issue row on the batch
	// transaction. Nil skips the audit.
	Audit func(ctx context.Context, tx pgx.Tx, batch gen.InvitationBatchRow, meta map[string]any) error
	// AuditOrder writes the v1.invitation.issue row of EACH order of the
	// batch, in the shape a single invitation writes. Nil skips it.
	AuditOrder func(ctx context.Context, tx pgx.Tx, order gen.OrderRow, meta map[string]any) error
	// Now is the clock; zero means time.Now().
	Now time.Time
}

// BatchResult is the committed batch, or the one an earlier upload with the
// same request_id wrote (Replayed).
type BatchResult struct {
	BatchID  uuid.UUID
	Replayed bool
}

// GuestError is a refusal of one guest's invitation inside the batch
// transaction. The whole batch was rolled back.
type GuestError struct {
	// Seq is the guest's position in the file.
	Seq int
	Err *IssueError
}

func (e *GuestError) Error() string { return fmt.Sprintf("guest %d: %s", e.Seq, e.Err.Error()) }
func (e *GuestError) Unwrap() error { return e.Err }

// RequestDigest is the idempotency key of an upload: the file AND the
// batch-wide delivery, since the same file with another delivery makes other
// letters. It is stored in invitation_batches.file_sha256.
func RequestDigest(file []byte, delivery string) string {
	h := sha256.New()
	h.Write(file)
	h.Write([]byte{0})
	h.Write([]byte(delivery))
	return hex.EncodeToString(h.Sum(nil))
}

var errBatchRace = errors.New("hinvitations: concurrent batch with the same request_id")

// batchReplayHit carries a replay found INSIDE the batch transaction (after
// the sessions row lock) out of it; the transaction is rolled back.
type batchReplayHit struct{ res BatchResult }

func (h *batchReplayHit) Error() string { return "hinvitations: batch already issued" }

// IssueBatch issues the whole upload in ONE transaction: every guest's
// order, the batch row and the audit are written together or not at all.
// The batch is the replay unit: the same request_id with the same file
// answers the first batch again. Refusals are *IssueError or *GuestError.
func IssueBatch(ctx context.Context, pool hcheckout.TxStarter, q *gen.Queries, in BatchInput) (BatchResult, error) {
	if pool == nil || q == nil {
		return BatchResult{}, errors.New("hinvitations: IssueBatch requires a pool and queries")
	}
	if len(in.Guests) == 0 {
		return BatchResult{}, refuse(http.StatusBadRequest, "invitation_batch.empty", "the file has no guests", nil)
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	if res, ok, err := batchReplay(ctx, q, in); err != nil || ok {
		return res, err
	}

	var out BatchResult
	err := txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("hinvitations: begin batch tx: %w", err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

		id, err := batchTx(ctx, tx, q.WithTx(tx), in)
		if err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			if isBatchRace(err) {
				return errBatchRace
			}
			return fmt.Errorf("hinvitations: commit batch: %w", err)
		}
		out = BatchResult{BatchID: id}
		return nil
	})
	var hit *batchReplayHit
	if errors.As(err, &hit) {
		return hit.res, nil
	}
	if errors.Is(err, errBatchRace) {
		res, ok, rerr := batchReplay(ctx, q, in)
		if rerr != nil {
			return BatchResult{}, rerr
		}
		if ok {
			return res, nil
		}
		return BatchResult{}, err
	}
	if err != nil {
		return BatchResult{}, err
	}
	return out, nil
}

func isBatchRace(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == batchRequestIndex
}

// batchReplay answers the batch of an earlier upload with the same
// request_id. A request_id reused for another session or another file is a
// client error, never a silent second answer.
func batchReplay(ctx context.Context, q *gen.Queries, in BatchInput) (BatchResult, bool, error) {
	prev, err := q.GetInvitationBatchByRequest(ctx, in.OrgID, in.RequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BatchResult{}, false, nil
	}
	if err != nil {
		return BatchResult{}, false, fmt.Errorf("hinvitations: batch request lookup: %w", err)
	}
	if prev.SessionID != in.SessionID || prev.FileSHA256 != in.FileSHA256 {
		return BatchResult{}, false, refuse(http.StatusConflict, "invitation_batch.request_id_reused",
			"this request_id was already used for another session or another file", nil)
	}
	return BatchResult{BatchID: prev.ID, Replayed: true}, true, nil
}

// batchTx is the batch transaction body. Lock order (AGENTS.md): the
// sessions row FIRST (seat_status_version bump), then every named seat of
// the file FOR UPDATE in seat_key order, then per guest the General
// Admission places (SKIP LOCKED) and inventory_ledger. A refusal anywhere
// rolls the whole file back.
func batchTx(ctx context.Context, tx pgx.Tx, txq *gen.Queries, in BatchInput) (uuid.UUID, error) {
	version, err := bumpSessionVersion(ctx, txq, in.SessionID)
	if err != nil {
		return uuid.Nil, err
	}
	// The same upload again, under the sessions row lock: a concurrent twin
	// that held it first has committed by now.
	if res, ok, err := batchReplay(ctx, txq, in); err != nil {
		return uuid.Nil, err
	} else if ok {
		return uuid.Nil, &batchReplayHit{res: res}
	}
	target, err := issueTarget(ctx, txq, in.OrgID, in.SessionID)
	if err != nil {
		return uuid.Nil, err
	}

	var keys []string
	places := 0
	for _, g := range in.Guests {
		keys = append(keys, g.SeatKeys...)
		places += len(g.SeatKeys)
		for _, l := range g.GA {
			places += int(l.Quantity)
		}
	}
	if places > MaxBatchPlaces {
		return uuid.Nil, refuse(http.StatusUnprocessableEntity, "invitation_batch.too_many_places",
			"one upload hands out at most 1000 places", map[string]any{"max": MaxBatchPlaces})
	}
	locked, err := lockSeats(ctx, txq, in.SessionID, keys)
	if err != nil {
		return uuid.Nil, err
	}
	byKey := make(map[string]takenPlace, len(locked))
	exclude := map[uuid.UUID]bool{}
	for _, p := range locked {
		byKey[p.seat.SeatKey] = p
		if p.seat.Kind == "ga_unit" {
			exclude[p.seat.ID] = true
		}
	}

	batch, err := txq.InsertInvitationBatch(ctx, in.OrgID, in.SessionID, in.IssuedBy,
		in.FileSHA256, in.RequestID, int32(in.Rows), int32(places)) //nolint:gosec // bounded by maxBatchRows / MaxBatchPlaces
	if err != nil {
		// The unique index is checked at the statement: the same request_id
		// on ANOTHER session (a twin on this session was serialized by the
		// sessions row lock and found by the replay lookup above).
		if isBatchRace(err) {
			return uuid.Nil, refuse(http.StatusConflict, "invitation_batch.request_id_reused",
				"this request_id was already used for another session or another file", nil)
		}
		return uuid.Nil, fmt.Errorf("hinvitations: insert batch: %w", err)
	}

	channelID, err := invitations.EnsureInternalChannel(ctx, txq, in.OrgID)
	if err != nil {
		return uuid.Nil, err
	}
	cache := &issueCache{channelID: channelID, amounts: map[uuid.UUID]int64{}}
	tiers := map[uuid.UUID]gen.TicketTierRow{}
	fromReserve := 0
	for _, g := range in.Guests {
		gin := IssueInput{
			OrgID:     in.OrgID,
			SessionID: in.SessionID,
			// One deterministic id per guest: the order's external_ref is
			// unique per channel, so a guest can never be written twice.
			RequestID: uuid.NewSHA1(in.RequestID, []byte(fmt.Sprintf("guest:%d", g.Seq))),
			SeatKeys:  g.SeatKeys,
			GA:        g.GA,
			Guest:     Guest{Name: g.Name, Email: g.Email, Phone: g.Phone, Locale: g.Locale},
			Delivery:  g.Delivery,
			Note:      g.Note,
			IssuedBy:  in.IssuedBy,
			Actor:     in.Actor,
			Now:       in.Now,
			BatchID:   &batch.ID,
			BatchSeq:  g.Seq,
			cache:     cache,
			Audit:     in.AuditOrder,
		}
		taken := make([]takenPlace, 0, len(g.SeatKeys))
		for _, k := range g.SeatKeys {
			taken = append(taken, byKey[k])
		}
		taken, err := allocateGA(ctx, txq, gin, taken, tiers, exclude)
		if err != nil {
			return uuid.Nil, guestErr(g.Seq, err)
		}
		w, err := writeInvitation(ctx, tx, txq, gin, RequestRef(gin.RequestID), version, target.EventID, taken, tiers)
		if err != nil {
			return uuid.Nil, guestErr(g.Seq, err)
		}
		fromReserve += w.FromReserve
	}

	if err := txq.SetInvitationBatchFromReserve(ctx, batch.ID, int32(fromReserve)); err != nil { //nolint:gosec // bounded by MaxBatchPlaces
		return uuid.Nil, fmt.Errorf("hinvitations: batch counts: %w", err)
	}
	batch.FromReserve = int32(fromReserve) //nolint:gosec // bounded by MaxBatchPlaces
	if in.Audit != nil {
		meta := map[string]any{
			"session_id":   in.SessionID.String(),
			"request_id":   in.RequestID.String(),
			"batch_id":     batch.ID.String(),
			"rows":         in.Rows,
			"guests":       len(in.Guests),
			"places":       places,
			"from_reserve": fromReserve,
		}
		if err := in.Audit(ctx, tx, batch, meta); err != nil {
			return uuid.Nil, fmt.Errorf("hinvitations: batch audit: %w", err)
		}
	}
	return batch.ID, nil
}

// guestErr tags a refusal with the guest it came from; anything that is not
// a business refusal (a database error) passes through unchanged so a
// deadlock still reaches txretry.
func guestErr(seq int, err error) error {
	var ie *IssueError
	if errors.As(err, &ie) {
		return &GuestError{Seq: seq, Err: ie}
	}
	return err
}
