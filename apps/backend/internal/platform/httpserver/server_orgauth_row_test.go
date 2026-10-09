// server_orgauth_row_test.go — the decision table of Server.rowOrgAccess,
// the guard behind the flat refund and payment-intent routes (PAY-00).
// No database: the membership branch is exercised through its fail-closed
// path only; the live-DB coverage is refund_org_isolation_integration_test.go.
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestRowOrgAccess_DecisionTable(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(discardWriter{}, nil))}
	org := uuid.New()
	other := uuid.New()

	service := func(orgID uuid.UUID) context.Context {
		return auth.WithActor(context.Background(), auth.Actor{
			ID: uuid.NewString(), Type: auth.ActorTypeService, OrgID: orgID.String(),
		})
	}
	user := auth.WithActor(context.Background(), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	superadmin := auth.WithSuperadminOrgAccess(user)

	cases := []struct {
		name     string
		ctx      context.Context
		write    bool
		reason   string
		want     bool
		wantCode int
		wantErr  string
	}{
		{name: "superadmin reads without a reason", ctx: superadmin, want: true},
		{name: "superadmin write needs X-Admin-Reason", ctx: superadmin, write: true, wantCode: http.StatusBadRequest, wantErr: "superadmin.missing_reason"},
		{name: "superadmin writes with a reason", ctx: superadmin, write: true, reason: "support ticket 1", want: true},
		{name: "api key of the organization", ctx: service(org), write: true, want: true},
		{name: "api key of another organization is the route's 404", ctx: service(other), wantCode: http.StatusNotFound, wantErr: "refund.not_found"},
		{name: "user without membership queries fails closed", ctx: user, wantCode: http.StatusNotFound, wantErr: "refund.not_found"},
		{name: "anonymous is the route's 404", ctx: context.Background(), wantCode: http.StatusNotFound, wantErr: "refund.not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/v1/refunds/x", nil).WithContext(tc.ctx)
			if tc.reason != "" {
				r.Header.Set("X-Admin-Reason", tc.reason)
			}
			got := s.rowOrgAccess(w, r, org, tc.write, "refund.not_found", "refund not found")
			if got != tc.want {
				t.Fatalf("allowed = %v, want %v (body %s)", got, tc.want, w.Body.String())
			}
			if tc.want {
				if w.Body.Len() != 0 {
					t.Fatalf("an allow must write nothing, got %s", w.Body.String())
				}
				return
			}
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %s: %v", w.Body.String(), err)
			}
			if body.Error.Code != tc.wantErr {
				t.Fatalf("code = %q, want %q", body.Error.Code, tc.wantErr)
			}
		})
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
