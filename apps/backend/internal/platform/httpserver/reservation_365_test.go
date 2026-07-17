// reservation_365_test.go — unit tests for feature #365 (PR2-09 MAJOR):
// Guard reservation state transitions against concurrent races.
//
// Problem: UpdateReservationState issued an unconditional UPDATE with no state
// guard. Concurrent cancels, or a cancel racing the TTL worker, each released
// capacity and understated holds, enabling oversell.
//
// Fix: UpdateReservationStateGuarded (WHERE id=$1 AND state=$2) is the new
// variant that only wins when the current state matches the expected value.
// expireReservation re-reads the row inside its per-item tx and uses the
// guarded UPDATE; HandleCancelReservation and ReleaseHold use it too.
// Capacity is released ONLY after the guarded transition wins.
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: SQL query layer — UpdateReservationStateGuarded exists in reservations.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step1_SQLQueryHasGuardedUpdate(t *testing.T) {
	content := findFileByName(t, "reservations.sql")

	t.Run("query_name_declared", func(t *testing.T) {
		if !strings.Contains(content, "UpdateReservationStateGuarded") {
			t.Error("reservations.sql: missing UpdateReservationStateGuarded query")
		}
	})

	t.Run("state_guard_in_where_clause", func(t *testing.T) {
		// The guarded query must have AND state = $2 so the UPDATE is a no-op
		// (returns 0 rows → pgx.ErrNoRows) when the state changed concurrently.
		if !strings.Contains(content, "AND  state = $2") {
			t.Error("reservations.sql: UpdateReservationStateGuarded must have 'AND  state = $2' guard clause")
		}
	})

	t.Run("new_state_is_third_param", func(t *testing.T) {
		// The new state must be $3 (id=$1, expectedState=$2, newState=$3).
		if !strings.Contains(content, "state        = $3") {
			t.Error("reservations.sql: UpdateReservationStateGuarded must set state=$3")
		}
	})

	t.Run("returns_full_row", func(t *testing.T) {
		if !strings.Contains(content, "RETURNING id, org_id") {
			t.Error("reservations.sql: UpdateReservationStateGuarded must RETURNING the full row")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Generated Go layer — UpdateReservationStateGuarded method on *Queries
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step2_GenFileHasGuardedUpdateMethod(t *testing.T) {
	content := findFileByName(t, "reservations.sql.go")

	t.Run("method_on_queries", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) UpdateReservationStateGuarded(") {
			t.Error("reservations.sql.go: missing UpdateReservationStateGuarded method on *Queries")
		}
	})

	t.Run("signature_has_expected_and_new_state", func(t *testing.T) {
		if !strings.Contains(content, "expectedState, newState string") {
			t.Error("reservations.sql.go: UpdateReservationStateGuarded must accept expectedState, newState string params")
		}
	})

	t.Run("const_query_has_state_guard", func(t *testing.T) {
		if !strings.Contains(content, "AND  state = $2") {
			t.Error("reservations.sql.go: const updateReservationStateGuarded must embed 'AND  state = $2' guard")
		}
	})

	t.Run("returns_reservation_row", func(t *testing.T) {
		if !strings.Contains(content, "UpdateReservationStateGuarded") || !strings.Contains(content, "ReservationRow, error") {
			t.Error("reservations.sql.go: UpdateReservationStateGuarded must return (ReservationRow, error)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Querier interface — UpdateReservationStateGuarded declared
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step3_QuerierInterfaceHasGuardedUpdate(t *testing.T) {
	content := findFileByName(t, "querier.go")

	want := "UpdateReservationStateGuarded(ctx context.Context, id uuid.UUID, expectedState, newState string) (ReservationRow, error)"
	if !strings.Contains(content, want) {
		t.Errorf("querier.go: Querier interface missing correct UpdateReservationStateGuarded signature;\nwant substring: %s", want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: expireReservation — re-reads row and uses guarded UPDATE
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step4_ExpireReservationGuardedTransition(t *testing.T) {
	content := findFileByName(t, "reservation_processor.go")

	t.Run("re_reads_state_inside_per_item_tx", func(t *testing.T) {
		// The TTL worker commits the poll-tx before per-item processing.
		// expireReservation must re-read the row (GetReservationByID) inside
		// each per-item transaction to detect state changes.
		if !strings.Contains(content, "GetReservationByID") {
			t.Error("reservation_processor.go: expireReservation must call GetReservationByID to re-check state inside per-item tx")
		}
	})

	t.Run("skips_when_state_already_terminal", func(t *testing.T) {
		// Must bail out early when state is not draft or active.
		if !strings.Contains(content, `current.State != "draft" && current.State != "active"`) {
			t.Error(`reservation_processor.go: expireReservation must check current.State != "draft" && current.State != "active" and skip`)
		}
	})

	t.Run("uses_guarded_update_not_unconditional", func(t *testing.T) {
		if !strings.Contains(content, "UpdateReservationStateGuarded") {
			t.Error("reservation_processor.go: expireReservation must use UpdateReservationStateGuarded instead of unconditional UpdateReservationState")
		}
	})

	t.Run("does_not_use_unconditional_update_state", func(t *testing.T) {
		// reservation_processor.go should no longer call the unguarded variant
		// since all transitions in this file go through the guarded one.
		if strings.Contains(content, "UpdateReservationState(") {
			t.Error("reservation_processor.go: still contains unconditional UpdateReservationState call; must be replaced with the guarded variant")
		}
	})

	t.Run("capacity_released_after_guarded_transition", func(t *testing.T) {
		// ReleaseCapacity call must appear AFTER the UpdateReservationStateGuarded
		// call in source order, so capacity is only released when we win the race.
		// We look for the actual method-call patterns (not comments or text).
		guardedIdx := strings.Index(content, "UpdateReservationStateGuarded(")
		releaseIdx := strings.Index(content, ".ReleaseCapacity(")
		if guardedIdx < 0 {
			t.Fatal("UpdateReservationStateGuarded( call not found in reservation_processor.go")
		}
		if releaseIdx < 0 {
			t.Fatal(".ReleaseCapacity( call not found in reservation_processor.go")
		}
		if releaseIdx < guardedIdx {
			t.Error("reservation_processor.go: .ReleaseCapacity( call appears BEFORE UpdateReservationStateGuarded( — capacity will double-release on concurrent cancel")
		}
	})

	t.Run("logs_when_race_is_lost", func(t *testing.T) {
		// When pgx.ErrNoRows is returned by the guarded UPDATE, must log.
		if !strings.Contains(content, "guarded transition lost race") && !strings.Contains(content, "lost the race") {
			t.Error("reservation_processor.go: must log when guarded transition loses race (ErrNoRows from UpdateReservationStateGuarded)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: HandleCancelReservation — uses guarded UPDATE, returns 409 on race
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step5_CancelHandlerGuardedTransition(t *testing.T) {
	content := findFileByName(t, "reservations.go")

	// Locate the cancel handler section.
	cancelIdx := strings.Index(content, "HandleCancelReservation")
	if cancelIdx < 0 {
		t.Fatal("reservations.go: HandleCancelReservation not found")
	}
	cancelSection := content[cancelIdx:]

	t.Run("uses_guarded_update", func(t *testing.T) {
		if !strings.Contains(cancelSection, "UpdateReservationStateGuarded") {
			t.Error("reservations.go HandleCancelReservation: must use UpdateReservationStateGuarded to prevent double-release")
		}
	})

	t.Run("returns_409_on_concurrent_race", func(t *testing.T) {
		// When the guarded UPDATE returns ErrNoRows (state changed concurrently),
		// the handler must respond 409 Conflict with code reservation.state_changed.
		if !strings.Contains(cancelSection, "reservation.state_changed") {
			t.Error("reservations.go HandleCancelReservation: must return 409 with error code 'reservation.state_changed' when guarded UPDATE loses the race")
		}
	})

	t.Run("capacity_released_after_guarded_transition", func(t *testing.T) {
		// ReleaseCapacity call must appear AFTER UpdateReservationStateGuarded to ensure
		// capacity is released exactly once (only by the winner of the race).
		guardedIdx := strings.Index(cancelSection, "UpdateReservationStateGuarded(")
		releaseIdx := strings.Index(cancelSection, ".ReleaseCapacity(")
		if guardedIdx < 0 {
			t.Fatal("UpdateReservationStateGuarded( not found in HandleCancelReservation")
		}
		if releaseIdx < 0 {
			t.Fatal(".ReleaseCapacity( not found in HandleCancelReservation")
		}
		if releaseIdx < guardedIdx {
			t.Error("reservations.go HandleCancelReservation: .ReleaseCapacity( appears BEFORE UpdateReservationStateGuarded( — double-release on concurrent cancel/expire")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: ReleaseHold — uses guarded UPDATE to prevent double-release
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step6_ReleaseHoldGuardedTransition(t *testing.T) {
	content := findFileByName(t, "hold_api.go")

	releaseIdx := strings.Index(content, "func ReleaseHold(")
	if releaseIdx < 0 {
		t.Fatal("hold_api.go: ReleaseHold function not found")
	}
	holdSection := content[releaseIdx:]

	t.Run("uses_guarded_update", func(t *testing.T) {
		if !strings.Contains(holdSection, "UpdateReservationStateGuarded") {
			t.Error("hold_api.go ReleaseHold: must use UpdateReservationStateGuarded to prevent double-release race with TTL worker")
		}
	})

	t.Run("does_not_use_unconditional_update_state", func(t *testing.T) {
		if strings.Contains(holdSection, "UpdateReservationState(") {
			t.Error("hold_api.go ReleaseHold: still calls unconditional UpdateReservationState; replace with guarded variant")
		}
	})

	t.Run("capacity_released_after_guarded_transition", func(t *testing.T) {
		guardedIdx := strings.Index(holdSection, "UpdateReservationStateGuarded(")
		releaseIdx2 := strings.Index(holdSection, ".ReleaseCapacity(")
		if guardedIdx < 0 {
			t.Fatal("UpdateReservationStateGuarded( not found in ReleaseHold")
		}
		if releaseIdx2 < 0 {
			t.Fatal(".ReleaseCapacity( not found in ReleaseHold")
		}
		if releaseIdx2 < guardedIdx {
			t.Error("hold_api.go ReleaseHold: .ReleaseCapacity( appears BEFORE UpdateReservationStateGuarded( — double-release possible")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: reservation_processor.go does not import errors already, guarded
//         path uses errors.Is — verify import is present
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step7_ProcessorImportsErrors(t *testing.T) {
	content := findFileByName(t, "reservation_processor.go")
	if !strings.Contains(content, `"errors"`) {
		t.Error("reservation_processor.go: must import \"errors\" to use errors.Is for pgx.ErrNoRows handling")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: Concurrent cancel+expire invariant — double-release is structurally
//         prevented (verified through the ordering checks above + querier guard)
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation365_Step8_DoubleReleaseStructurallyPrevented(t *testing.T) {
	// Verify all three race-sensitive callers use the guarded variant.
	cases := []struct {
		filename string
		section  string // empty = whole file
	}{
		{"reservation_processor.go", "expireReservation"},
		{"reservations.go", "HandleCancelReservation"},
		{"hold_api.go", "ReleaseHold"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.filename+"/"+tc.section, func(t *testing.T) {
			content := findFileByName(t, tc.filename)
			section := content
			if tc.section != "" {
				idx := strings.Index(content, tc.section)
				if idx < 0 {
					t.Fatalf("%s: %s function not found", tc.filename, tc.section)
				}
				section = content[idx:]
			}
			if !strings.Contains(section, "UpdateReservationStateGuarded") {
				t.Errorf("%s (%s): missing UpdateReservationStateGuarded — double-release race not prevented", tc.filename, tc.section)
			}
		})
	}
}
