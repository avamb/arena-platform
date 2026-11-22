package hauth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newLimitTestHandler() *Handler {
	return New(nil, nil, nil, "", "", "", 1, 0)
}

func sendAccountRequest(h http.HandlerFunc, method, target, body, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func assertRateLimited(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(int(loginRateLimitWindow.Seconds())) {
		t.Fatalf("Retry-After = %q", got)
	}
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "auth.rate_limited" {
		t.Fatalf("code = %q, want auth.rate_limited", env.Error.Code)
	}
	if v, ok := env.Error.Details["retry_after_seconds"].(float64); !ok || int(v) != int(loginRateLimitWindow.Seconds()) {
		t.Fatalf("retry_after_seconds = %v", env.Error.Details["retry_after_seconds"])
	}
}

func TestRegister_LimitedPerClientAddress(t *testing.T) {
	h := newLimitTestHandler()
	for i := 0; i < registerPerIPAttempts; i++ {
		// An empty password is refused after the limit check, without bcrypt.
		body := fmt.Sprintf(`{"email":"a%d@example.test","password":""}`, i)
		if rec := sendAccountRequest(h.Register, http.MethodPost, "/v1/auth/register", body, "198.51.100.7:1000"); rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i, rec.Code)
		}
	}
	rec := sendAccountRequest(h.Register, http.MethodPost, "/v1/auth/register", `{"email":"last@example.test","password":""}`, "198.51.100.7:1000")
	assertRateLimited(t, rec)

	other := sendAccountRequest(h.Register, http.MethodPost, "/v1/auth/register", `{"email":"other@example.test","password":""}`, "198.51.100.8:1000")
	if other.Code != http.StatusBadRequest {
		t.Fatalf("another address: status = %d, want 400", other.Code)
	}
}

func TestRegister_LimitedPerEmail(t *testing.T) {
	h := newLimitTestHandler()
	for i := 0; i < registerPerEmailAttempts; i++ {
		remote := fmt.Sprintf("198.51.100.%d:1000", 10+i)
		if rec := sendAccountRequest(h.Register, http.MethodPost, "/v1/auth/register", `{"email":"Same@Example.test","password":""}`, remote); rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i, rec.Code)
		}
	}
	rec := sendAccountRequest(h.Register, http.MethodPost, "/v1/auth/register", `{"email":"same@example.test ","password":""}`, "198.51.100.99:1000")
	assertRateLimited(t, rec)
}

func TestPasswordResetRequest_LimitedPerClientAddressAndEmail(t *testing.T) {
	t.Run("per address", func(t *testing.T) {
		h := newLimitTestHandler()
		for i := 0; i < resetRequestPerIPAttempts; i++ {
			body := fmt.Sprintf(`{"email":"r%d@example.test"}`, i)
			// No database: the request passes the limit and answers 503.
			if rec := sendAccountRequest(h.PasswordResetRequest, http.MethodPost, "/v1/auth/password-reset/request", body, "203.0.113.5:1"); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("request %d: status = %d, want 503", i, rec.Code)
			}
		}
		assertRateLimited(t, sendAccountRequest(h.PasswordResetRequest, http.MethodPost, "/v1/auth/password-reset/request", `{"email":"x@example.test"}`, "203.0.113.5:1"))
	})
	t.Run("per e-mail", func(t *testing.T) {
		h := newLimitTestHandler()
		for i := 0; i < resetRequestPerEmailAttempts; i++ {
			remote := fmt.Sprintf("203.0.113.%d:1", 20+i)
			if rec := sendAccountRequest(h.PasswordResetRequest, http.MethodPost, "/v1/auth/password-reset/request", `{"email":"victim@example.test"}`, remote); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("request %d: status = %d, want 503", i, rec.Code)
			}
		}
		assertRateLimited(t, sendAccountRequest(h.PasswordResetRequest, http.MethodPost, "/v1/auth/password-reset/request", `{"email":"VICTIM@example.test"}`, "203.0.113.99:1"))
	})
}

func TestPasswordResetConfirm_LimitedPerClientAddress(t *testing.T) {
	h := newLimitTestHandler()
	for i := 0; i < resetConfirmPerIPAttempts; i++ {
		if rec := sendAccountRequest(h.PasswordResetConfirm, http.MethodPost, "/v1/auth/password-reset/confirm", `{}`, "192.0.2.44:9"); rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i, rec.Code)
		}
	}
	assertRateLimited(t, sendAccountRequest(h.PasswordResetConfirm, http.MethodPost, "/v1/auth/password-reset/confirm", `{}`, "192.0.2.44:9"))
}

func TestVerifyEmail_LimitedPerClientAddress(t *testing.T) {
	h := newLimitTestHandler()
	for i := 0; i < verifyPerIPAttempts; i++ {
		if rec := sendAccountRequest(h.VerifyEmail, http.MethodGet, "/v1/auth/verify", "", "192.0.2.55:9"); rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i, rec.Code)
		}
	}
	assertRateLimited(t, sendAccountRequest(h.VerifyEmail, http.MethodGet, "/v1/auth/verify", "", "192.0.2.55:9"))
}

func TestAccountLimits_ZeroHandlerAllows(t *testing.T) {
	h := &Handler{}
	for i := 0; i < verifyPerIPAttempts+5; i++ {
		if rec := sendAccountRequest(h.VerifyEmail, http.MethodGet, "/v1/auth/verify", "", "192.0.2.66:9"); rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i, rec.Code)
		}
	}
}
