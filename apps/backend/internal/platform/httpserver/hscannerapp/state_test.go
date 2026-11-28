package hscannerapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

func TestRequireActiveDevice(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := RequireActiveDevice(next)
	run := func(withDevice bool, d scannerapp.Device) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/scanner-app/scans", nil)
		if withDevice {
			r = r.WithContext(scannerapp.WithDevice(r.Context(), d))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := run(true, scannerapp.Device{State: scannerapp.StateActive}); w.Code != http.StatusTeapot {
		t.Fatalf("active device: %d, want the next handler", w.Code)
	}
	w := run(true, scannerapp.Device{State: scannerapp.StateUploadOnly})
	if w.Code != http.StatusForbidden {
		t.Fatalf("upload-only device: %d, want 403", w.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Error.Code != "scanner_app.pass_closed" {
		t.Fatalf("body = %s, want scanner_app.pass_closed", w.Body.String())
	}
	if w := run(false, scannerapp.Device{}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no device: %d, want 401", w.Code)
	}
}
