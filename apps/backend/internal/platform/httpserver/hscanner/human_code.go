// human_code.go — SEAT-C4 human-code fallback for online scan paths.
//
// PLATFORM-authority barcodes carry the static_qr credential payload as
// their external_ref (the barcode is registered via POST /v1/barcodes with
// the ticket's ticket_credentials.payload — the same string encoded into
// the PDF QR code; see the scan_events.credential_code column comment in
// migration 0055). When gate staff cannot scan the QR image they type the
// 8-character human code printed under it instead. Online endpoints that
// resolve platform barcodes therefore accept the human code as an alias:
// if the direct external_ref lookup misses, and the presented value
// normalizes to a syntactically valid human code, the code is resolved to
// its credential and the barcode lookup is retried with the credential
// payload. Non-platform authorities never take the fallback — their
// external references are opaque strings owned by the issuing system.
package hscanner

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/humancode"
)

// PlatformAuthorityType is the barcode_authorities.type value for barcodes
// issued by the Arena platform itself. Only this authority stores the
// static_qr credential payload as external_ref, so only this authority
// participates in the human-code fallback.
const PlatformAuthorityType = "platform"

// HumanCodeCredentialGetter is the narrow query surface the human-code
// fallback needs: resolve a canonical (normalized) human code to its
// static_qr ticket credential row. *gen.Queries satisfies it.
type HumanCodeCredentialGetter interface {
	GetCredentialByHumanCode(ctx context.Context, humanCode string) (gen.TicketCredentialRow, error)
}

// ResolveHumanCodeExternalRef maps a scanner-presented external_ref that
// failed the direct barcode lookup to the credential payload registered as
// the PLATFORM-authority barcode external_ref.
//
// It returns (payload, true) when all of the following hold:
//   - authorityType is the platform authority (non-platform authorities are
//     never rewritten);
//   - externalRef normalizes to a syntactically valid human code
//     (humancode.Normalize handles case, hyphens/spaces and the Crockford
//     look-alike aliases I→1 L→1 O→0);
//   - a static_qr credential carries that canonical code.
//
// In every other case it returns ("", false) and the caller keeps its
// original not-found handling. Unexpected (non-ErrNoRows) lookup failures
// are logged and degrade to the not-found path rather than failing the
// scan with a 5xx: the human code is a best-effort alias, the barcode
// itself remains the source of truth.
func ResolveHumanCodeExternalRef(
	ctx context.Context,
	q HumanCodeCredentialGetter,
	logger *slog.Logger,
	authorityType string,
	externalRef string,
) (string, bool) {
	if q == nil || authorityType != PlatformAuthorityType {
		return "", false
	}
	canonical, ok := humancode.Normalize(externalRef)
	if !ok {
		return "", false
	}
	cred, err := q.GetCredentialByHumanCode(ctx, canonical)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && logger != nil {
			// Never log the full code: like the QR payload it is a
			// bearer-quality string (see CredentialPrefixForLog).
			logger.Error("scanner: human-code credential lookup failed",
				slog.String("human_code_prefix", canonical[:4]+"…"),
				slog.String("error", err.Error()),
			)
		}
		return "", false
	}
	return cred.Payload, true
}
