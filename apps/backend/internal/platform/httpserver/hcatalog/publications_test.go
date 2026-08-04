// publications_test.go — unit tests for AB-43 FK error classification.
package hcatalog

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestClassifyPublicationFKError_AB43 verifies that a 23503 foreign-key
// violation on event_publications maps to a specific 404 code and message
// per constraint (feed token, city, event) instead of a generic 500.
func TestClassifyPublicationFKError_AB43(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		constraint string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "feed_token_fk_violation_maps_to_404_feed_token_not_found",
			constraint: "event_publications_feed_token_id_fkey",
			wantStatus: http.StatusNotFound,
			wantCode:   "publication.feed_token_not_found",
		},
		{
			name:       "city_fk_violation_maps_to_404_city_not_found",
			constraint: "event_publications_city_id_fkey",
			wantStatus: http.StatusNotFound,
			wantCode:   "publication.city_not_found",
		},
		{
			name:       "event_fk_violation_maps_to_404_event_not_found",
			constraint: "event_publications_event_id_fkey",
			wantStatus: http.StatusNotFound,
			wantCode:   "publication.event_not_found",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pgErr := &pgconn.PgError{
				Code:           pgForeignKeyViolation,
				ConstraintName: tc.constraint,
			}
			status, code, msg, ok := ClassifyPublicationFKError(pgErr)
			if !ok {
				t.Fatalf("expected FK classifier to match %s", tc.constraint)
			}
			if status != tc.wantStatus {
				t.Errorf("status: got %d want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q want %q", code, tc.wantCode)
			}
			if msg == "" {
				t.Errorf("message must be non-empty for code %q", code)
			}
		})
	}
}

// TestClassifyPublicationFKError_UnrecognisedErrors verifies the classifier
// returns ok=false for anything that is not a 23503 FK violation on a known
// event_publications constraint. Callers must fall through to their 5xx
// branch in that case.
func TestClassifyPublicationFKError_UnrecognisedErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{"nil error", nil},
		{"plain go error", errors.New("boom")},
		{"unrelated pg error code", &pgconn.PgError{Code: "23505" /* unique */, ConstraintName: "event_publications_unique"}},
		{"fk violation on unknown constraint", &pgconn.PgError{Code: pgForeignKeyViolation, ConstraintName: "some_other_fkey"}},
		{"wrapped fk violation on unknown constraint", fmt.Errorf("wrapping: %w", &pgconn.PgError{Code: pgForeignKeyViolation, ConstraintName: "orphan_fkey"})},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, _, ok := ClassifyPublicationFKError(tc.err); ok {
				t.Fatalf("expected classifier to skip err=%v", tc.err)
			}
		})
	}
}

// TestClassifyPublicationFKError_WrappedError verifies the classifier
// respects errors.As-style unwrapping (pgx often returns wrapped errors).
func TestClassifyPublicationFKError_WrappedError(t *testing.T) {
	t.Parallel()
	pgErr := &pgconn.PgError{
		Code:           pgForeignKeyViolation,
		ConstraintName: "event_publications_feed_token_id_fkey",
	}
	wrapped := fmt.Errorf("PublishEvent failed: %w", pgErr)
	_, code, _, ok := ClassifyPublicationFKError(wrapped)
	if !ok {
		t.Fatalf("expected classifier to unwrap and match")
	}
	if code != "publication.feed_token_not_found" {
		t.Errorf("code: got %q want publication.feed_token_not_found", code)
	}
}
