// scan_human_code_test.go — SEAT-C4 human-code fallback on POST /v1/scan.
//
// Mirrors the hscanner validate-path tests: a human code typed at the gate
// (hyphens / lowercase / Crockford aliases) resolves a PLATFORM-authority
// barcode via its static_qr credential payload; unknown codes keep the
// original not-found envelope; non-platform authorities never consult
// ticket_credentials.
package hbarcode

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

// scanRow implements pgx.Row over a pre-baked value slice. A nil entry
// leaves the corresponding scan destination at its zero value.
type scanRow struct {
	vals []any
	err  error
}

func (r *scanRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.vals) {
		return fmt.Errorf("scanRow: %d destinations for %d values", len(dest), len(r.vals))
	}
	for i, v := range r.vals {
		if v == nil {
			continue
		}
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

// scanFakeDB implements gen.DBTX for the POST /v1/scan flow: one authority,
// one credential (by canonical human code), one active barcode (by
// external_ref), and the MarkBarcodeScanned transition.
type scanFakeDB struct {
	authority gen.BarcodeAuthorityRow

	credentialCode    string
	credentialPayload string

	barcodeRef string
	barcode    gen.BarcodeRow

	credentialLookups int
	barcodeLookups    []string
}

func (f *scanFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("scanFakeDB: unexpected Exec")
}

func (f *scanFakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("scanFakeDB: unexpected Query")
}

func (f *scanFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	barcodeVals := func(b gen.BarcodeRow) []any {
		return []any{
			b.ID, b.AuthorityID, b.ExternalRef, b.TicketID, b.Status,
			b.ScannedAt, b.CreatedAt, b.UpdatedAt,
		}
	}
	switch {
	case strings.Contains(sql, "FROM   barcode_authorities"):
		a := f.authority
		return &scanRow{vals: []any{a.ID, a.Type, a.Label, a.CreatedAt}}

	case strings.Contains(sql, "FROM   ticket_credentials"):
		f.credentialLookups++
		if code, _ := args[0].(string); code == f.credentialCode && f.credentialCode != "" {
			hc := f.credentialCode
			return &scanRow{vals: []any{
				uuid.New(), uuid.New(), "static_qr", f.credentialPayload, &hc, time.Now(), nil,
			}}
		}
		return &scanRow{err: pgx.ErrNoRows}

	case strings.Contains(sql, "FROM   barcodes"):
		ref, _ := args[1].(string)
		f.barcodeLookups = append(f.barcodeLookups, ref)
		if ref == f.barcodeRef && f.barcodeRef != "" {
			return &scanRow{vals: barcodeVals(f.barcode)}
		}
		return &scanRow{err: pgx.ErrNoRows}

	case strings.Contains(sql, "UPDATE barcodes"):
		// MarkBarcodeScanned: return the barcode in its scanned state.
		b := f.barcode
		b.Status = "scanned"
		now := time.Now()
		b.ScannedAt = &now
		return &scanRow{vals: barcodeVals(b)}
	}
	return &scanRow{err: fmt.Errorf("scanFakeDB: unexpected SQL %q", sql)}
}

func TestHandleScan_HumanCodeFallback(t *testing.T) {
	const (
		canonical = "M7KT201V"
		payload   = "0f60cc7f0a2b4bd0a53fbe32a1b7de1a0f60cc7f0a2b4bd0a53fbe32a1b7de1a"
	)

	cases := []struct {
		name          string
		authorityType string
		externalRef   string
		barcodeRef    string

		wantStatus        int
		wantExternalRef   string
		wantErrorCode     string
		wantCredLookups   int
		wantBarcodeLookup []string
	}{
		{
			name:              "aliased human code scans platform barcode",
			authorityType:     "platform",
			externalRef:       "m7kt-2olv", // O->0, L->1 aliases of canonical
			barcodeRef:        payload,
			wantStatus:        200,
			wantExternalRef:   payload,
			wantCredLookups:   1,
			wantBarcodeLookup: []string{"m7kt-2olv", payload},
		},
		{
			name:              "unknown human code keeps the not-found envelope",
			authorityType:     "platform",
			externalRef:       "ZZZZ-ZZZZ",
			barcodeRef:        payload,
			wantStatus:        404,
			wantErrorCode:     "barcode.not_found",
			wantCredLookups:   1,
			wantBarcodeLookup: []string{"ZZZZ-ZZZZ"},
		},
		{
			name:              "non-platform authority never takes the fallback",
			authorityType:     "guest_list",
			externalRef:       "m7kt-2olv",
			barcodeRef:        payload,
			wantStatus:        404,
			wantErrorCode:     "barcode.not_found",
			wantCredLookups:   0,
			wantBarcodeLookup: []string{"m7kt-2olv"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authorityID := uuid.New()
			db := &scanFakeDB{
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
			h := New(q, q, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

			body := fmt.Sprintf(`{"external_ref":%q,"authority_type":%q}`, tc.externalRef, tc.authorityType)
			req := httptest.NewRequest("POST", "/v1/scan", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.HandleScan(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == 200 {
				var resp scanResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.ExternalRef != tc.wantExternalRef {
					t.Errorf("external_ref = %q, want %q", resp.ExternalRef, tc.wantExternalRef)
				}
				if resp.Status != "scanned" {
					t.Errorf("status = %q, want %q", resp.Status, "scanned")
				}
			} else if tc.wantErrorCode != "" && !strings.Contains(rec.Body.String(), tc.wantErrorCode) {
				t.Errorf("body %s does not contain error code %q", rec.Body.String(), tc.wantErrorCode)
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
