// human_code_test.go — SEAT-C4 human-code fallback on the online scan paths.
//
// Pins three behaviors of the fallback wired into POST /v1/scanner/validate
// (and, via the shared ResolveHumanCodeExternalRef helper, POST /v1/scan):
//
//  1. A human code typed with hyphens / lowercase / Crockford look-alike
//     aliases resolves to its static_qr credential and the barcode lookup
//     is retried with the credential payload as external_ref.
//  2. An unknown (but well-formed) code leaves the original not-found
//     envelope unchanged.
//  3. Non-platform authorities never consult ticket_credentials — their
//     external references are opaque strings owned by the issuing system.
package hscanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─── ResolveHumanCodeExternalRef unit tests ──────────────────────────────────

// fakeCredGetter implements HumanCodeCredentialGetter: it recognizes exactly
// one canonical code and records every lookup it receives.
type fakeCredGetter struct {
	code    string
	payload string
	err     error // forced non-ErrNoRows failure when non-nil
	calls   []string
}

func (f *fakeCredGetter) GetCredentialByHumanCode(_ context.Context, humanCode string) (gen.TicketCredentialRow, error) {
	f.calls = append(f.calls, humanCode)
	if f.err != nil {
		return gen.TicketCredentialRow{}, f.err
	}
	if humanCode == f.code {
		return gen.TicketCredentialRow{Type: "static_qr", Payload: f.payload}, nil
	}
	return gen.TicketCredentialRow{}, pgx.ErrNoRows
}

func TestResolveHumanCodeExternalRef(t *testing.T) {
	const (
		canonical = "M7KT201V" // valid Crockford, contains 0 and 1 for alias cases
		payload   = "0f60cc7f0a2b4bd0a53fbe32a1b7de1a0f60cc7f0a2b4bd0a53fbe32a1b7de1a"
	)

	cases := []struct {
		name          string
		authorityType string
		externalRef   string
		getterErr     error
		wantPayload   string
		wantOK        bool
		wantLookups   []string
	}{
		{
			name:          "canonical code resolves",
			authorityType: PlatformAuthorityType,
			externalRef:   canonical,
			wantPayload:   payload,
			wantOK:        true,
			wantLookups:   []string{canonical},
		},
		{
			name:          "hyphenated lowercase input resolves",
			authorityType: PlatformAuthorityType,
			externalRef:   "m7kt-201v",
			wantPayload:   payload,
			wantOK:        true,
			wantLookups:   []string{canonical},
		},
		{
			name:          "crockford aliases O->0 L->1 resolve",
			authorityType: PlatformAuthorityType,
			externalRef:   "m7kt-2olv",
			wantPayload:   payload,
			wantOK:        true,
			wantLookups:   []string{canonical},
		},
		{
			name:          "alias I->1 with spaces resolves",
			authorityType: PlatformAuthorityType,
			externalRef:   " M7KT 2OIV ",
			wantPayload:   payload,
			wantOK:        true,
			wantLookups:   []string{canonical},
		},
		{
			name:          "unknown code misses",
			authorityType: PlatformAuthorityType,
			externalRef:   "ZZZZ-ZZZZ",
			wantOK:        false,
			wantLookups:   []string{"ZZZZZZZZ"},
		},
		{
			name:          "non-platform authority never consults credentials",
			authorityType: "legacy_bil24",
			externalRef:   canonical,
			wantOK:        false,
			wantLookups:   nil,
		},
		{
			name:          "non-code input (QR token) skips the lookup",
			authorityType: PlatformAuthorityType,
			externalRef:   payload, // 64-char hex — not an 8-char code
			wantOK:        false,
			wantLookups:   nil,
		},
		{
			name:          "unexpected lookup failure degrades to not-found",
			authorityType: PlatformAuthorityType,
			externalRef:   canonical,
			getterErr:     errors.New("connection reset"),
			wantOK:        false,
			wantLookups:   []string{canonical},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getter := &fakeCredGetter{code: canonical, payload: payload, err: tc.getterErr}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))

			got, ok := ResolveHumanCodeExternalRef(
				context.Background(), getter, logger, tc.authorityType, tc.externalRef,
			)

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.wantPayload {
				t.Errorf("payload = %q, want %q", got, tc.wantPayload)
			}
			if !reflect.DeepEqual(getter.calls, tc.wantLookups) {
				t.Errorf("credential lookups = %v, want %v", getter.calls, tc.wantLookups)
			}
		})
	}
}

func TestResolveHumanCodeExternalRef_NilGetter(t *testing.T) {
	got, ok := ResolveHumanCodeExternalRef(
		context.Background(), nil, nil, PlatformAuthorityType, "M7KT-201V",
	)
	if ok || got != "" {
		t.Fatalf("nil getter: got (%q, %v), want (\"\", false)", got, ok)
	}
}

// ─── HandleScannerValidate handler-level tests ───────────────────────────────

// fakeRow implements pgx.Row over a pre-baked value slice. A nil entry
// leaves the corresponding scan destination at its zero value.
type fakeRow struct {
	vals []any
	err  error
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.vals) {
		return fmt.Errorf("fakeRow: %d destinations for %d values", len(dest), len(r.vals))
	}
	for i, v := range r.vals {
		if v == nil {
			continue
		}
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

// validateFakeDB implements gen.DBTX for the validate flow. It serves the
// authority row unconditionally, exactly one credential (by canonical human
// code) and exactly one barcode (by external_ref), recording lookups so
// tests can assert which queries ran.
type validateFakeDB struct {
	authority gen.BarcodeAuthorityRow

	credentialCode    string // canonical human code that resolves
	credentialPayload string

	barcodeRef string // external_ref that resolves
	barcode    gen.BarcodeRow

	credentialLookups int
	barcodeLookups    []string
}

func (f *validateFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("validateFakeDB: unexpected Exec")
}

func (f *validateFakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("validateFakeDB: unexpected Query")
}

func (f *validateFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM   barcode_authorities"):
		a := f.authority
		return &fakeRow{vals: []any{a.ID, a.Type, a.Label, a.CreatedAt}}

	case strings.Contains(sql, "FROM   ticket_credentials"):
		f.credentialLookups++
		if code, _ := args[0].(string); code == f.credentialCode && f.credentialCode != "" {
			hc := f.credentialCode
			return &fakeRow{vals: []any{
				uuid.New(), uuid.New(), "static_qr", f.credentialPayload, &hc, time.Now(), nil,
			}}
		}
		return &fakeRow{err: pgx.ErrNoRows}

	case strings.Contains(sql, "FROM   barcodes"):
		ref, _ := args[1].(string)
		f.barcodeLookups = append(f.barcodeLookups, ref)
		if ref == f.barcodeRef && f.barcodeRef != "" {
			b := f.barcode
			return &fakeRow{vals: []any{
				b.ID, b.AuthorityID, b.ExternalRef, b.TicketID, b.Status,
				b.ScannedAt, b.CreatedAt, b.UpdatedAt,
			}}
		}
		return &fakeRow{err: pgx.ErrNoRows}
	}
	return &fakeRow{err: fmt.Errorf("validateFakeDB: unexpected SQL %q", sql)}
}

// allowAllRL satisfies RateLimiter and never throttles.
type allowAllRL struct{}

func (allowAllRL) CheckIP(string) bool      { return true }
func (allowAllRL) CheckSession(string) bool { return true }

func TestHandleScannerValidate_HumanCodeFallback(t *testing.T) {
	const (
		canonical = "M7KT201V"
		payload   = "0f60cc7f0a2b4bd0a53fbe32a1b7de1a0f60cc7f0a2b4bd0a53fbe32a1b7de1a"
	)

	cases := []struct {
		name          string
		authorityType string
		externalRef   string
		barcodeRef    string // external_ref stored on the one known barcode

		wantStatus        int
		wantExternalRef   string // asserted on 200 responses
		wantErrorCode     string // asserted on error envelopes
		wantCredLookups   int
		wantBarcodeLookup []string
	}{
		{
			name:              "hyphenated lowercase aliased human code resolves platform barcode",
			authorityType:     "platform",
			externalRef:       "m7kt-2olv", // O->0, L->1 aliases of canonical
			barcodeRef:        payload,
			wantStatus:        200,
			wantExternalRef:   payload,
			wantCredLookups:   1,
			wantBarcodeLookup: []string{"m7kt-2olv", payload},
		},
		{
			name:              "direct QR payload still resolves without credential lookup",
			authorityType:     "platform",
			externalRef:       payload,
			barcodeRef:        payload,
			wantStatus:        200,
			wantExternalRef:   payload,
			wantCredLookups:   0,
			wantBarcodeLookup: []string{payload},
		},
		{
			name:              "unknown human code keeps the not-found envelope",
			authorityType:     "platform",
			externalRef:       "ZZZZ-ZZZZ",
			barcodeRef:        payload,
			wantStatus:        404,
			wantErrorCode:     "scanner.barcode_not_found",
			wantCredLookups:   1,
			wantBarcodeLookup: []string{"ZZZZ-ZZZZ"},
		},
		{
			name:              "non-platform authority never takes the fallback",
			authorityType:     "legacy_bil24",
			externalRef:       "m7kt-2olv",
			barcodeRef:        payload,
			wantStatus:        404,
			wantErrorCode:     "scanner.barcode_not_found",
			wantCredLookups:   0,
			wantBarcodeLookup: []string{"m7kt-2olv"},
		},
		{
			name:              "non-platform direct ref untouched",
			authorityType:     "legacy_bil24",
			externalRef:       "LEGACY-REF-001",
			barcodeRef:        "LEGACY-REF-001",
			wantStatus:        200,
			wantExternalRef:   "LEGACY-REF-001",
			wantCredLookups:   0,
			wantBarcodeLookup: []string{"LEGACY-REF-001"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authorityID := uuid.New()
			db := &validateFakeDB{
				authority: gen.BarcodeAuthorityRow{
					ID:        authorityID,
					Type:      tc.authorityType,
					Label:     "test authority",
					CreatedAt: time.Now(),
				},
				credentialCode:    canonical,
				credentialPayload: payload,
				barcodeRef:        tc.barcodeRef,
				barcode: gen.BarcodeRow{
					ID:          uuid.New(),
					AuthorityID: authorityID,
					ExternalRef: tc.barcodeRef,
					Status:      "active",
					CreatedAt:   time.Now(),
					UpdatedAt:   time.Now(),
				},
			}
			q := gen.New(db)
			h := New(q, q, nil, nil, allowAllRL{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

			body := fmt.Sprintf(`{"external_ref":%q,"authority_type":%q}`, tc.externalRef, tc.authorityType)
			req := httptest.NewRequest("POST", "/v1/scanner/validate", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.HandleScannerValidate(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == 200 {
				var resp ValidateBarcodeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.ExternalRef != tc.wantExternalRef {
					t.Errorf("external_ref = %q, want %q", resp.ExternalRef, tc.wantExternalRef)
				}
				if !resp.Valid {
					t.Errorf("valid = false, want true")
				}
			} else if tc.wantErrorCode != "" {
				if !strings.Contains(rec.Body.String(), tc.wantErrorCode) {
					t.Errorf("body %s does not contain error code %q", rec.Body.String(), tc.wantErrorCode)
				}
			}
			if db.credentialLookups != tc.wantCredLookups {
				t.Errorf("credential lookups = %d, want %d", db.credentialLookups, tc.wantCredLookups)
			}
			if !reflect.DeepEqual(db.barcodeLookups, tc.wantBarcodeLookup) {
				t.Errorf("barcode lookups = %v, want %v", db.barcodeLookups, tc.wantBarcodeLookup)
			}
		})
	}
}
