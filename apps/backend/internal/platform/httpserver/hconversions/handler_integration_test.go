//go:build integration

package hconversions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConversionSettings_WriteOnlySecrets_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	orgID := uuid.New()
	nonce := orgID.String()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "ConvSet "+nonce, "convset-"+nonce); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID) }()

	h := New(pool, nil)
	call := func(method, body string, fn http.HandlerFunc) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/organizations/"+orgID.String()+"/conversion-settings", strings.NewReader(body))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("org_id", orgID.String())
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		fn(w, req)
		return w
	}
	w := call(http.MethodPatch, `{"meta_pixel_id":"1352226752766944","meta_capi_token":"super-secret-token","ga4_measurement_id":"G-M5HFQ11W7V","ga4_api_secret":"ga-api-secret-1"}`, h.HandlePatch)
	if w.Code != 200 || strings.Contains(w.Body.String(), "super-secret") || strings.Contains(w.Body.String(), "ga-api-secret") ||
		!strings.Contains(w.Body.String(), `"meta_capi_token_set":true`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	// Absent keys keep, null clears.
	w = call(http.MethodPatch, `{"ga4_api_secret":null}`, h.HandlePatch)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ga4_api_secret_set":false`) || !strings.Contains(w.Body.String(), `"meta_capi_token_set":true`) {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodPatch, `{"meta_pixel_id":"abc"}`, h.HandlePatch); w.Code != 422 {
		t.Fatalf("bad pixel: %d", w.Code)
	}
	if w = call(http.MethodPatch, `{"nope":1}`, h.HandlePatch); w.Code != 400 {
		t.Fatalf("unknown key: %d", w.Code)
	}
	if w = call(http.MethodGet, ``, h.HandleGet); w.Code != 200 || !strings.Contains(w.Body.String(), "1352226752766944") {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
}
