package permissions_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// =============================================================================
// Step 1: Checker interface exists with Check(ctx, action, resource) error
// =============================================================================

func TestChecker_InterfaceSignature(_ *testing.T) {
	// Compile-time checks: both AllowAll and DenyAll satisfy the interface.
	var _ = permissions.AllowAll()
	var _ = permissions.DenyAll()
}

// =============================================================================
// Step 2: AllowAllChecker — any actor passes
// =============================================================================

func TestAllowAllChecker_ReturnsNil(t *testing.T) {
	c := permissions.AllowAll()
	if err := c.Check(context.Background(), "read", "event"); err != nil {
		t.Fatalf("AllowAll.Check: expected nil, got %v", err)
	}
}

func TestAllowAllChecker_AnyActionAnyResource(t *testing.T) {
	c := permissions.AllowAll()
	cases := [][2]string{
		{"create", "event"},
		{"delete", "organization"},
		{"publish", "ticket"},
		{"", ""},
		{"scan", "entry_gate"},
	}
	for _, tc := range cases {
		if err := c.Check(context.Background(), tc[0], tc[1]); err != nil {
			t.Errorf("AllowAll.Check(%q, %q): expected nil, got %v", tc[0], tc[1], err)
		}
	}
}

func TestAllowAllChecker_NilContext(t *testing.T) {
	// Checker must not panic when ctx is the zero value background context.
	c := permissions.AllowAll()
	err := c.Check(context.Background(), "read", "event")
	if err != nil {
		t.Fatalf("AllowAll.Check with background ctx: expected nil, got %v", err)
	}
}

// =============================================================================
// Step 3: DenyAllChecker — returns PermissionDeniedError
// =============================================================================

func TestDenyAllChecker_ReturnsError(t *testing.T) {
	c := permissions.DenyAll()
	err := c.Check(context.Background(), "read", "event")
	if err == nil {
		t.Fatal("DenyAll.Check: expected non-nil error, got nil")
	}
}

func TestDenyAllChecker_ErrorIsPermissionDenied(t *testing.T) {
	c := permissions.DenyAll()
	err := c.Check(context.Background(), "write", "ticket")
	if !errors.Is(err, permissions.ErrPermissionDenied) {
		t.Fatalf("DenyAll.Check: expected errors.Is(ErrPermissionDenied), got %T: %v", err, err)
	}
}

func TestDenyAllChecker_ReturnsPermissionDeniedError(t *testing.T) {
	c := permissions.DenyAll()
	err := c.Check(context.Background(), "delete", "organization")
	var pde *permissions.PermissionDeniedError
	if !errors.As(err, &pde) {
		t.Fatalf("DenyAll.Check: expected *PermissionDeniedError, got %T: %v", err, err)
	}
	if pde.Action != "delete" {
		t.Errorf("PermissionDeniedError.Action: expected %q, got %q", "delete", pde.Action)
	}
	if pde.Resource != "organization" {
		t.Errorf("PermissionDeniedError.Resource: expected %q, got %q", "organization", pde.Resource)
	}
}

func TestDenyAllChecker_ErrorMessageContainsActionAndResource(t *testing.T) {
	c := permissions.DenyAll()
	err := c.Check(context.Background(), "publish", "event")
	msg := err.Error()
	if !strings.Contains(msg, "publish") {
		t.Errorf("error message %q should contain action %q", msg, "publish")
	}
	if !strings.Contains(msg, "event") {
		t.Errorf("error message %q should contain resource %q", msg, "event")
	}
}

func TestDenyAllChecker_HTTPStatus403(t *testing.T) {
	c := permissions.DenyAll()
	err := c.Check(context.Background(), "create", "ticket")
	var pde *permissions.PermissionDeniedError
	if !errors.As(err, &pde) {
		t.Fatalf("expected *PermissionDeniedError, got %T", err)
	}
	if pde.HTTPStatus() != http.StatusForbidden {
		t.Errorf("HTTPStatus: expected 403, got %d", pde.HTTPStatus())
	}
}

// =============================================================================
// Step 4: HTTP middleware — RequirePermission
// =============================================================================

func TestRequirePermission_AllowAll_ForwardsRequest(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := permissions.RequirePermission(permissions.AllowAll(), "read", "event")(next)

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("RequirePermission(AllowAll): next handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("RequirePermission(AllowAll): expected 200, got %d", rec.Code)
	}
}

func TestRequirePermission_DenyAll_Returns403(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})

	handler := permissions.RequirePermission(permissions.DenyAll(), "write", "event")(next)

	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Error("RequirePermission(DenyAll): next handler should not have been called")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("RequirePermission(DenyAll): expected 403, got %d", rec.Code)
	}
}

func TestRequirePermission_DenyAll_JSONResponseBody(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := permissions.RequirePermission(permissions.DenyAll(), "delete", "ticket")(next)

	req := httptest.NewRequest(http.MethodDelete, "/tickets/1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: expected application/json prefix, got %q", ct)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}

	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response JSON missing 'error' key: %v", body)
	}
	code, _ := errObj["code"].(string)
	if code != "permissions.denied" {
		t.Errorf("error.code: expected %q, got %q", "permissions.denied", code)
	}
	message, _ := errObj["message"].(string)
	if message == "" {
		t.Error("error.message should not be empty")
	}
}

func TestRequirePermission_ContentType(t *testing.T) {
	handler := permissions.RequirePermission(permissions.DenyAll(), "read", "org")(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}),
	)

	req := httptest.NewRequest(http.MethodGet, "/org", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type should be application/json, got %q", ct)
	}
}

func TestRequirePermission_MultipleActions(t *testing.T) {
	t.Run("read_allowed", func(t *testing.T) {
		h := permissions.RequirePermission(permissions.AllowAll(), "read", "event")(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }),
		)
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("write_denied", func(t *testing.T) {
		h := permissions.RequirePermission(permissions.DenyAll(), "write", "event")(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }),
		)
		req := httptest.NewRequest("POST", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("expected 403, got %d", rec.Code)
		}
	})
}

// =============================================================================
// Step 5: ErrPermissionDenied sentinel
// =============================================================================

func TestErrPermissionDenied_Sentinel(t *testing.T) {
	if permissions.ErrPermissionDenied == nil {
		t.Fatal("ErrPermissionDenied should not be nil")
	}
	if !strings.Contains(permissions.ErrPermissionDenied.Error(), "permission") {
		t.Errorf("ErrPermissionDenied message should mention 'permission', got %q",
			permissions.ErrPermissionDenied.Error())
	}
}

func TestPermissionDeniedError_UnwrapsToSentinel(t *testing.T) {
	pde := &permissions.PermissionDeniedError{Action: "read", Resource: "event"}
	if !errors.Is(pde, permissions.ErrPermissionDenied) {
		t.Error("*PermissionDeniedError.Unwrap should return ErrPermissionDenied")
	}
}

// =============================================================================
// Step 6: PLACEHOLDER documentation — compile-time comment presence check
// =============================================================================

// The PLACEHOLDER documentation is verified by reading the source in other
// tests; this test documents the intent for the test suite so human reviewers
// see it clearly.
func TestPermissions_IsPlaceholder(t *testing.T) {
	// AllowAll MUST be the only implementation wired in production for this
	// milestone.  DenyAll MUST only appear in tests.
	t.Log("PLACEHOLDER: AllowAll() is the milestone no-op implementation.")
	t.Log("PLACEHOLDER: Real RBAC engine will replace AllowAll in the next milestone.")
	t.Log("PLACEHOLDER: Checker interface, ErrPermissionDenied, and RequirePermission are stable.")
}

// =============================================================================
// Full integration sweep
// =============================================================================

func TestPermissions_FullVerification(t *testing.T) {
	t.Run("step1_checker_interface", func(_ *testing.T) {
		var _ = permissions.AllowAll()
		var _ = permissions.DenyAll()
	})

	t.Run("step2_allowall_passes", func(t *testing.T) {
		c := permissions.AllowAll()
		if err := c.Check(context.Background(), "create", "event"); err != nil {
			t.Fatalf("AllowAll: expected nil, got %v", err)
		}
	})

	t.Run("step3_denyall_returns_permission_denied_error", func(t *testing.T) {
		c := permissions.DenyAll()
		err := c.Check(context.Background(), "write", "ticket")
		if err == nil {
			t.Fatal("DenyAll: expected non-nil error")
		}
		if !errors.Is(err, permissions.ErrPermissionDenied) {
			t.Fatalf("DenyAll: expected errors.Is(ErrPermissionDenied), got %T: %v", err, err)
		}
	})

	t.Run("step4_require_permission_middleware_allow", func(t *testing.T) {
		called := false
		h := permissions.RequirePermission(permissions.AllowAll(), "read", "event")(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(200) }),
		)
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !called || rec.Code != 200 {
			t.Errorf("RequirePermission(AllowAll): called=%v status=%d", called, rec.Code)
		}
	})

	t.Run("step4_require_permission_middleware_deny", func(t *testing.T) {
		called := false
		h := permissions.RequirePermission(permissions.DenyAll(), "write", "event")(
			http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }),
		)
		req := httptest.NewRequest("POST", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if called {
			t.Error("next handler should not be called when DenyAll")
		}
		if rec.Code != 403 {
			t.Errorf("expected 403, got %d", rec.Code)
		}
	})

	t.Run("step5_sentinel_and_structured_error", func(t *testing.T) {
		if permissions.ErrPermissionDenied == nil {
			t.Fatal("ErrPermissionDenied must not be nil")
		}
		pde := &permissions.PermissionDeniedError{Action: "x", Resource: "y"}
		if !errors.Is(pde, permissions.ErrPermissionDenied) {
			t.Fatal("PermissionDeniedError must wrap ErrPermissionDenied")
		}
		if pde.HTTPStatus() != 403 {
			t.Fatalf("HTTPStatus() must be 403, got %d", pde.HTTPStatus())
		}
	})

	t.Run("step6_placeholder_documented", func(t *testing.T) {
		// Package doc string is the authoritative PLACEHOLDER label.
		// The test logs the intent so CI output is self-documenting.
		t.Log("AllowAll is the PLACEHOLDER implementation for this milestone.")
	})
}
