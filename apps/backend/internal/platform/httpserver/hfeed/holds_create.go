// holds_create.go — POST /v1/public/feeds/{feed_token}/holds: hold seats at
// the click (spec 08_architecture/67, sections 4.1, 4.6 and 6).
package hfeed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// createHoldRequest is the body of POST .../holds.
type createHoldRequest struct {
	SessionID string   `json:"session_id"`
	SeatKeys  []string `json:"seat_keys"`
	HoldToken string   `json:"hold_token"`
	DeviceID  string   `json:"device_id"`
	ReholdOf  string   `json:"rehold_of"`
}

// holdCreateInput is the checked request with everything read on the pool
// before the transaction.
type holdCreateInput struct {
	sessionID  uuid.UUID
	orgID      uuid.UUID
	channelID  uuid.UUID
	seatKeys   []string
	tokenHash  []byte
	reholdHash []byte
	deviceHash []byte
	ipHash     []byte
	policy     holdpolicy.Policy
	now        time.Time
}

// holdCreateOutcome is what the transaction did.
type holdCreateOutcome struct {
	row       gen.PublicHoldRow
	seatKeys  []string
	conflicts []map[string]string
	created   bool
}

// HandleCreateHold serves POST /v1/public/feeds/{feed_token}/holds.
func (hh *HoldsHandler) HandleCreateHold(w http.ResponseWriter, r *http.Request) {
	if !hh.ready(w, r) {
		hh.h.metrics.RecordHoldCreated(observability.HoldResultDisabled)
		return
	}
	feedToken := chi.URLParam(r, "feed_token")
	if !hh.h.enforceRateLimit(w, r, "feed.rate_limited", hh.h.rl.CheckFeedToken, feedToken) {
		hh.h.metrics.RecordHoldCreated(observability.HoldResultRejected)
		return
	}
	next := http.HandlerFunc(hh.createHold)
	if hh.cfg.Idempotency == nil {
		next.ServeHTTP(w, r)
		return
	}
	hh.cfg.Idempotency(next).ServeHTTP(w, r)
}

func (hh *HoldsHandler) createHold(w http.ResponseWriter, r *http.Request) {
	in, ok := hh.checkCreate(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var out holdCreateOutcome
	err := txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		tx, err := hh.h.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("begin hold tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		o, err := hh.createHoldTx(ctx, hh.h.reservationQueries.WithTx(tx), in)
		if err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit hold tx: %w", err)
		}
		out = o
		return nil
	})
	if err != nil {
		hh.h.metrics.RecordHoldCreated(hh.writeHoldFailure(w, r, "create", err))
		return
	}
	answer := buildHoldAnswer(out.row, hh.tokenFor(out.row.ID), out.seatKeys, false, in.now)
	answer.Conflicts = out.conflicts
	status, result := http.StatusOK, observability.HoldResultAdded
	if out.created {
		status, result = http.StatusCreated, observability.HoldResultCreated
	}
	hh.h.metrics.RecordHoldCreated(result)
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, status, answer)
}

// checkCreate reads and checks the request and everything the transaction
// needs from the pool. It answers and returns false on a refusal.
func (hh *HoldsHandler) checkCreate(w http.ResponseWriter, r *http.Request) (holdCreateInput, bool) {
	reject := func(status int, code, msg, result string, details map[string]any) (holdCreateInput, bool) {
		if details != nil {
			httputil.WriteJSON(w, status, httputil.ErrorEnvelopeWithDetails(code, msg, r, details))
		} else {
			httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
		}
		hh.h.metrics.RecordHoldCreated(result)
		return holdCreateInput{}, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, HoldsMaxBody))
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return reject(http.StatusBadRequest, "hold.invalid_body", "Send the hold as JSON.", observability.HoldResultRejected, nil)
	}
	var req createHoldRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return reject(http.StatusBadRequest, "hold.invalid_body", "We could not read the request body.", observability.HoldResultRejected, nil)
	}
	sessionID, err := uuid.Parse(strings.TrimSpace(req.SessionID))
	if err != nil {
		return reject(http.StatusBadRequest, "hold.invalid_session", "Send session_id as a UUID.", observability.HoldResultRejected,
			map[string]any{"field": "session_id"})
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if len(deviceID) > holdMaxDeviceIDLen {
		return reject(http.StatusBadRequest, "hold.invalid_device", "device_id is too long.", observability.HoldResultRejected,
			map[string]any{"field": "device_id"})
	}
	reholdOf := strings.TrimSpace(req.ReholdOf)
	if len(req.SeatKeys) > holdMaxSeatKeysPerRequest {
		return reject(http.StatusUnprocessableEntity, "hold.too_many_seats", "One hold can have fewer seats.", observability.HoldResultTooManySeats,
			map[string]any{"max_seats": holdMaxSeatKeysPerRequest})
	}
	seatKeys, dup, normErr := hcheckout.NormalizeSeatKeys(req.SeatKeys)
	if normErr != nil {
		return reject(http.StatusBadRequest, "hold.invalid_seats", "Each seat can appear only once in seat_keys.", observability.HoldResultRejected,
			map[string]any{"field": "seat_keys", "seat_key": dup})
	}
	if len(seatKeys) == 0 && reholdOf == "" {
		return reject(http.StatusBadRequest, "hold.invalid_seats", "Choose at least one seat: send a seat_key in seat_keys.", observability.HoldResultRejected,
			map[string]any{"field": "seat_keys"})
	}

	ctx := r.Context()
	feedToken := chi.URLParam(r, "feed_token")
	checkCtx, err := hh.h.publicFeedQueries.GetPublicCheckoutContext(ctx, feedToken, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reject(http.StatusForbidden, "feed.session_not_on_feed", "This session is not on sale here. Check the link.", observability.HoldResultRejected, nil)
		}
		hh.h.logger.Error("public_holds: context lookup failed", slog.String("error", err.Error()))
		return reject(http.StatusServiceUnavailable, "hold.failed", "We could not select the seat. Try again.", observability.HoldResultError, nil)
	}
	if gateErr := hcheckout.CheckSessionSellable(ctx, hh.h.reservationQueries, sessionID, time.Now().UTC()); gateErr != nil {
		hcheckout.WriteCategoryGateError(w, r, gateErr)
		hh.h.metrics.RecordHoldCreated(observability.HoldResultRejected)
		return holdCreateInput{}, false
	}
	mode, err := hh.h.reservationQueries.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reject(http.StatusNotFound, "reservation.session_not_found", "We cannot find this session.", observability.HoldResultRejected, nil)
		}
		hh.h.logger.Error("public_holds: admission mode lookup failed", slog.String("error", err.Error()))
		return reject(http.StatusServiceUnavailable, "hold.failed", "We could not select the seat. Try again.", observability.HoldResultError, nil)
	}
	if mode.AdmissionMode == hcheckout.AdmissionGeneralAdmission {
		return reject(http.StatusUnprocessableEntity, "hold.not_seated", "This session has no seating plan.", observability.HoldResultRejected,
			map[string]any{"admission_mode": mode.AdmissionMode})
	}
	// The profile is read on the pool, before the transaction (spec 67
	// section 7.1); an unreadable one falls back to standard.
	policy, perr := holdpolicy.ForSession(ctx, hh.h.reservationQueries, sessionID)
	if perr != nil {
		hh.h.logger.Warn("public_holds: hold policy unreadable, using standard",
			slog.String("session_id", sessionID.String()), slog.String("error", perr.Error()))
	}

	ipHash := hh.ipHash(hh.h.clientIP(r))
	deviceHash := hh.deviceHash(deviceID)
	if deviceHash == nil {
		// A click without device_id must not escape the per-device limits,
		// the pause and the inherited deadline: the address stands in for the
		// device, whether or not the address limits are switched on.
		deviceHash = ipHash
	}
	in := holdCreateInput{
		sessionID:  sessionID,
		orgID:      checkCtx.OrgID,
		channelID:  checkCtx.SalesChannelID,
		seatKeys:   seatKeys,
		deviceHash: deviceHash,
		ipHash:     ipHash,
		policy:     policy,
		now:        hh.now(),
	}
	if t := strings.TrimSpace(req.HoldToken); t != "" {
		in.tokenHash = holdTokenHash(t)
	}
	if reholdOf != "" {
		in.reholdHash = holdTokenHash(reholdOf)
	}
	return in, true
}

// createHoldTx is the hold transaction. Lock order: the visitor's advisory
// locks (device, then address) → the hold row when adding to one → sessions
// → session_seats → inventory_ledger (hcheckout partial helpers).
func (hh *HoldsHandler) createHoldTx(ctx context.Context, q *gen.Queries, in holdCreateInput) (holdCreateOutcome, error) {
	// Limits are checked under these locks: without them twenty parallel
	// requests of one device would all pass "fewer than two" (section 4.1).
	if in.deviceHash != nil {
		if err := q.HoldAdvisoryXactLock(ctx, advisoryKey(in.deviceHash)); err != nil {
			return holdCreateOutcome{}, fmt.Errorf("device lock: %w", err)
		}
	}
	if hh.cfg.IPLimitEnabled && in.ipHash != nil {
		if err := q.HoldAdvisoryXactLock(ctx, advisoryKey(in.ipHash)); err != nil {
			return holdCreateOutcome{}, fmt.Errorf("address lock: %w", err)
		}
	}

	if in.tokenHash != nil {
		row, err := q.LockPublicHoldByTokenHash(ctx, in.tokenHash)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return holdCreateOutcome{}, errHoldNotFound
			}
			return holdCreateOutcome{}, fmt.Errorf("lock hold: %w", err)
		}
		if row.SessionID != in.sessionID || row.ChannelID != in.channelID {
			return holdCreateOutcome{}, errHoldNotFound
		}
		if !holdOpen(row.State) || !row.ExpiresAt.After(in.now) || row.ClosingStartedAt != nil {
			return holdCreateOutcome{}, errHoldExpired
		}
		return hh.addToHoldTx(ctx, q, in, row)
	}
	if in.deviceHash != nil && in.reholdHash == nil {
		row, err := q.LockOpenDeviceHoldOnSession(ctx, in.sessionID, in.deviceHash)
		switch {
		case err == nil && row.ChannelID == in.channelID:
			// A second tab, a lost answer, a retry: the device's open hold
			// takes the seats (section 4.1).
			return hh.addToHoldTx(ctx, q, in, row)
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return holdCreateOutcome{}, fmt.Errorf("device hold lookup: %w", err)
		}
	}
	return hh.newHoldTx(ctx, q, in)
}

// addToHoldTx adds the requested seats to an open, locked hold.
func (hh *HoldsHandler) addToHoldTx(ctx context.Context, q *gen.Queries, in holdCreateInput, row gen.PublicHoldRow) (holdCreateOutcome, error) {
	inPayment, err := q.PublicHoldHasOpenOrder(ctx, row.ID)
	if err != nil {
		return holdCreateOutcome{}, fmt.Errorf("open order lookup: %w", err)
	}
	if inPayment {
		return holdCreateOutcome{}, errHoldInPayment
	}
	current, err := q.ListReservationSeats(ctx, row.ID)
	if err != nil {
		return holdCreateOutcome{}, fmt.Errorf("list hold seats: %w", err)
	}
	have := make(map[string]struct{}, len(current))
	for _, s := range current {
		have[s.SeatKey] = struct{}{}
	}
	var newKeys []string
	for _, k := range in.seatKeys {
		if _, ok := have[k]; !ok {
			newKeys = append(newKeys, k)
		}
	}
	pol := holdPolicyOf(row.HoldPolicy)
	if len(current)+len(newKeys) > pol.MaxSeatsPerHold {
		return holdCreateOutcome{}, tooManySeats(pol)
	}
	var paused []map[string]string
	if len(newKeys) > 0 && row.ReholdOf == nil {
		var retry int
		newKeys, paused, retry, err = hh.applyPause(ctx, q, in, pol, newKeys)
		if err != nil {
			return holdCreateOutcome{}, err
		}
		if len(newKeys) == 0 {
			return holdCreateOutcome{}, reholdPaused(paused, retry)
		}
	}
	if len(newKeys) == 0 {
		return holdCreateOutcome{row: row, seatKeys: seatKeysOf(current)}, nil
	}
	_, conflicts, err := hcheckout.ExtendHoldPartialTx(ctx, q, hcheckout.HoldMutationInput{
		ReservationID: row.ID, SeatKeys: newKeys, Now: in.now,
	})
	if err != nil {
		return holdCreateOutcome{}, mapHoldMutationError(err, paused)
	}
	return hh.finishTx(ctx, q, row.ID, append(paused, conflicts...), false)
}

// newHoldTx creates a new hold, or the hold of "hold again" (section 4.6).
func (hh *HoldsHandler) newHoldTx(ctx context.Context, q *gen.Queries, in holdCreateInput) (holdCreateOutcome, error) {
	pol := in.policy
	if in.deviceHash != nil {
		n, err := q.CountOpenPublicHoldsByDevice(ctx, in.deviceHash)
		if err != nil {
			return holdCreateOutcome{}, fmt.Errorf("count device holds: %w", err)
		}
		if n >= int64(pol.MaxActivePerDevice) {
			return holdCreateOutcome{}, tooManyActive(pol.MaxActivePerDevice)
		}
	}
	if hh.cfg.IPLimitEnabled && in.ipHash != nil {
		n, err := q.CountOpenPublicHoldsByIP(ctx, in.ipHash)
		if err != nil {
			return holdCreateOutcome{}, fmt.Errorf("count address holds: %w", err)
		}
		if n >= int64(pol.MaxActivePerIP) {
			return holdCreateOutcome{}, tooManyActive(pol.MaxActivePerIP)
		}
	}

	seats := in.seatKeys
	var reholdOf *uuid.UUID
	if in.reholdHash != nil {
		old, err := hh.checkReholdTx(ctx, q, in)
		if err != nil {
			return holdCreateOutcome{}, err
		}
		id := old.ID
		reholdOf = &id
		oldSeats := holdSeatKeysOf(old.HoldPolicy)
		if len(seats) == 0 {
			seats, _, _ = hcheckout.NormalizeSeatKeys(oldSeats)
		} else {
			// "Hold again" returns the seats of the old hold only.
			had := make(map[string]struct{}, len(oldSeats))
			for _, k := range oldSeats {
				had[k] = struct{}{}
			}
			for _, k := range seats {
				if _, ok := had[k]; !ok {
					return holdCreateOutcome{}, refuse(http.StatusUnprocessableEntity, "hold.rehold_foreign_seat",
						"Hold again takes only the seats of the expired hold. Select other seats as a new hold.",
						observability.HoldResultRejected,
						map[string]any{"field": "seat_keys", "seat_key": k})
				}
			}
		}
		if len(seats) == 0 {
			return holdCreateOutcome{}, reholdUnavailable()
		}
	}
	if len(seats) > pol.MaxSeatsPerHold {
		return holdCreateOutcome{}, tooManySeats(pol)
	}

	var paused []map[string]string
	if reholdOf == nil {
		var retry int
		var err error
		seats, paused, retry, err = hh.applyPause(ctx, q, in, pol, seats)
		if err != nil {
			return holdCreateOutcome{}, err
		}
		if len(seats) == 0 {
			return holdCreateOutcome{}, reholdPaused(paused, retry)
		}
	}

	// A new hold of the same device on the same session soon after the last
	// one closed keeps that one's select deadline and used extensions, so
	// "remove all and select again" does not restart the clock (section 6.3).
	// "Hold again" starts the select step anew.
	deadline := in.now.Add(pol.Select)
	var extUsed int16
	if reholdOf == nil && in.deviceHash != nil {
		d, used, err := q.GetInheritableSelectDeadline(ctx, in.sessionID, in.deviceHash, in.now.Add(-pol.ReholdPause))
		switch {
		case err == nil && d.After(in.now):
			deadline, extUsed = d, used
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return holdCreateOutcome{}, fmt.Errorf("inherited deadline lookup: %w", err)
		}
	}

	res, conflicts, err := hcheckout.CreateSeatedHoldPartialTx(ctx, q, hcheckout.SeatedHoldInput{
		OrgID:     in.orgID,
		ChannelID: in.channelID,
		SessionID: in.sessionID,
		SeatKeys:  seats,
		ExpiresAt: deadline,
	})
	if err != nil {
		return holdCreateOutcome{}, mapHoldMutationError(err, paused)
	}
	snapshot, err := json.Marshal(pol)
	if err != nil {
		return holdCreateOutcome{}, fmt.Errorf("policy snapshot: %w", err)
	}
	if err := q.SetPublicHoldFields(ctx, gen.SetPublicHoldFieldsParams{
		ID:                res.Reservation.ID,
		HoldTokenHash:     holdTokenHash(hh.tokenFor(res.Reservation.ID)),
		VisitorDeviceHash: in.deviceHash,
		VisitorIPHash:     in.ipHash,
		SelectDeadline:    deadline,
		ExtensionsUsed:    extUsed,
		ReholdOf:          reholdOf,
		HoldPolicy:        snapshot,
	}); err != nil {
		return holdCreateOutcome{}, fmt.Errorf("write hold fields: %w", err)
	}
	return hh.finishTx(ctx, q, res.Reservation.ID, append(paused, conflicts...), true)
}

// checkReholdTx finds and checks the expired hold of "hold again": the same
// device and session, expired at most ten minutes ago, never held again and
// not itself a "hold again" (once per chain, section 4.6). The old row is
// locked so two requests cannot both use it.
func (hh *HoldsHandler) checkReholdTx(ctx context.Context, q *gen.Queries, in holdCreateInput) (gen.PublicHoldRow, error) {
	if in.deviceHash == nil {
		return gen.PublicHoldRow{}, errHoldNotFound
	}
	old, err := q.LockPublicHoldByTokenHash(ctx, in.reholdHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.PublicHoldRow{}, errHoldNotFound
		}
		return gen.PublicHoldRow{}, fmt.Errorf("lock old hold: %w", err)
	}
	if !bytes.Equal(old.VisitorDeviceHash, in.deviceHash) || old.SessionID != in.sessionID || old.ChannelID != in.channelID {
		return gen.PublicHoldRow{}, errHoldNotFound
	}
	if old.ReholdOf != nil {
		return gen.PublicHoldRow{}, reholdUsed()
	}
	n, err := q.CountReholdsOf(ctx, old.ID)
	if err != nil {
		return gen.PublicHoldRow{}, fmt.Errorf("count reholds: %w", err)
	}
	if n > 0 {
		return gen.PublicHoldRow{}, reholdUsed()
	}
	if old.State != "expired" || old.ExpiredAt == nil || old.ExpiredAt.Before(in.now.Add(-holdReholdWindow)) {
		return gen.PublicHoldRow{}, reholdUnavailable()
	}
	return old, nil
}

// applyPause drops the seats this visitor may not hold yet: the seats of the
// device's holds on this session that expired unpaid within the pause
// (section 6.3). The address pause applies only to a request without a
// device id, and only with the address limits switched on. It returns the
// seats left, the dropped ones as conflicts with status "paused", and the
// seconds until the last of them is free to hold.
func (hh *HoldsHandler) applyPause(ctx context.Context, q *gen.Queries, in holdCreateInput, pol holdpolicy.Policy, seats []string) ([]string, []map[string]string, int, error) {
	if pol.ReholdPause <= 0 || len(seats) == 0 {
		return seats, nil, 0, nil
	}
	since := in.now.Add(-pol.ReholdPause)
	var rows []gen.PausedSeatKeyRow
	var err error
	switch {
	case in.deviceHash != nil:
		rows, err = q.ListPausedSeatKeysByDevice(ctx, in.deviceHash, in.sessionID, since)
	case hh.cfg.IPLimitEnabled && in.ipHash != nil:
		rows, err = q.ListPausedSeatKeysByIP(ctx, in.ipHash, in.sessionID, since)
	default:
		return seats, nil, 0, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("pause lookup: %w", err)
	}
	if len(rows) == 0 {
		return seats, nil, 0, nil
	}
	until := make(map[string]time.Time, len(rows))
	for _, p := range rows {
		until[p.SeatKey] = p.ExpiredAt.Add(pol.ReholdPause)
	}
	var left []string
	var paused []map[string]string
	retry := 0
	for _, k := range seats {
		u, ok := until[k]
		if !ok || !u.After(in.now) {
			left = append(left, k)
			continue
		}
		paused = append(paused, map[string]string{"seat_key": k, "status": "paused"})
		if s := int(math.Ceil(u.Sub(in.now).Seconds())); s > retry {
			retry = s
		}
	}
	return left, paused, retry, nil
}

// finishTx records the hold's seats in its snapshot and reads the hold back.
func (hh *HoldsHandler) finishTx(ctx context.Context, q *gen.Queries, id uuid.UUID, conflicts []map[string]string, created bool) (holdCreateOutcome, error) {
	seats, err := q.ListReservationSeats(ctx, id)
	if err != nil {
		return holdCreateOutcome{}, fmt.Errorf("list hold seats: %w", err)
	}
	keys := seatKeysOf(seats)
	if err := q.SetPublicHoldSeatKeys(ctx, id, keys); err != nil {
		return holdCreateOutcome{}, fmt.Errorf("record hold seats: %w", err)
	}
	full, err := q.LockPublicHoldByTokenHash(ctx, holdTokenHash(hh.tokenFor(id)))
	if err != nil {
		return holdCreateOutcome{}, fmt.Errorf("reread hold: %w", err)
	}
	sortConflicts(conflicts)
	return holdCreateOutcome{row: full, seatKeys: keys, conflicts: conflicts, created: created}, nil
}

func sortConflicts(c []map[string]string) {
	sort.SliceStable(c, func(i, j int) bool { return c[i]["seat_key"] < c[j]["seat_key"] })
}

// mapHoldMutationError turns the hcheckout errors into refusals.
func mapHoldMutationError(err error, paused []map[string]string) error {
	var conflictErr *hcheckout.SeatConflictsError
	if errors.As(err, &conflictErr) {
		conflicts := append(append([]map[string]string{}, paused...), conflictErr.Conflicts...)
		sortConflicts(conflicts)
		return refuse(http.StatusConflict, "hold.seats_taken",
			"Someone has just taken this seat. Choose other seats.", observability.HoldResultSeatsTaken,
			map[string]any{"conflicts": conflicts})
	}
	var capErr *hcheckout.CapacityError
	if errors.As(err, &capErr) {
		return refuse(http.StatusConflict, "hold.seats_taken",
			"There are no free seats left. Choose other seats.", observability.HoldResultSeatsTaken,
			map[string]any{"conflicts": paused})
	}
	var notMutable *hcheckout.NotMutableError
	if errors.As(err, &notMutable) || errors.Is(err, hcheckout.ErrHoldNotFound) {
		return errHoldExpired
	}
	if errors.Is(err, hcheckout.ErrHoldSeatsNotSupported) {
		return refuse(http.StatusUnprocessableEntity, "hold.not_seated", "This session has no seating plan.", observability.HoldResultRejected, nil)
	}
	if errors.Is(err, hcheckout.ErrHoldSessionNotFound) {
		return refuse(http.StatusNotFound, "reservation.session_not_found", "We cannot find this session.", observability.HoldResultRejected, nil)
	}
	if errors.Is(err, hcheckout.ErrHoldInvalidInput) {
		return refuse(http.StatusBadRequest, "hold.invalid_seats", "Choose at least one seat: send a seat_key in seat_keys.", observability.HoldResultRejected, nil)
	}
	if hcheckout.CategoryGateErrorCode(err) != "" {
		gateErr := err
		return &holdRefusal{
			result: observability.HoldResultRejected,
			code:   hcheckout.CategoryGateErrorCode(err),
			write:  func(w http.ResponseWriter, r *http.Request) { hcheckout.WriteCategoryGateError(w, r, gateErr) },
		}
	}
	return err
}

func tooManySeats(pol holdpolicy.Policy) *holdRefusal {
	return refuse(http.StatusUnprocessableEntity, "hold.too_many_seats",
		fmt.Sprintf("One hold can have up to %d seats.", pol.MaxSeatsPerHold), observability.HoldResultTooManySeats,
		map[string]any{"max_seats": pol.MaxSeatsPerHold})
}

func tooManyActive(limit int) *holdRefusal {
	return refuse(http.StatusTooManyRequests, "hold.too_many_active",
		"You already have open holds. Pay for one or cancel it.", observability.HoldResultTooManyActive,
		map[string]any{"max_active": limit})
}

func reholdPaused(paused []map[string]string, retry int) *holdRefusal {
	if retry < 1 {
		retry = 1
	}
	ref := refuse(http.StatusConflict, "hold.rehold_paused",
		"You can select these seats again later. Select other seats.", observability.HoldResultReholdPaused,
		map[string]any{"retry_after_seconds": retry, "conflicts": paused})
	ref.retryAfter = retry
	return ref
}

func reholdUsed() *holdRefusal {
	return refuse(http.StatusConflict, "hold.rehold_used",
		"These seats were already held again once. Select the seats again.", observability.HoldResultReholdUsed, nil)
}

func reholdUnavailable() *holdRefusal {
	return refuse(http.StatusConflict, "hold.rehold_unavailable",
		"This hold cannot be held again. Select the seats again.", observability.HoldResultExpired, nil)
}
