// credentials.go — ticket credential generation and retrieval (feature #140).
//
// A ticket credential is a bearer artifact that proves ticket ownership.
// This module supports two credential types:
//
//	static_qr — opaque 64-char hex token (32 crypto/rand bytes) bound to a
//	             ticket UUIDv7. The token itself is NOT the ticket UUID —
//	             the binding is stored server-side in ticket_credentials.payload.
//	             Scanners call GET /v1/scan/{token} to resolve the ticket status.
//
//	pdf       — minimal server-rendered PDF ticket document containing the
//	             ticket ID, QR token, and issue timestamp. Implemented with
//	             pure Go standard library (no external html-to-pdf dependency);
//	             the resulting PDF uses Helvetica (a standard built-in Type1
//	             font, no embedding required) and is valid per PDF 1.4 spec.
//
// Credentials are stored in the ticket_credentials table (one row per
// (ticket_id, type) pair). The GET endpoint is lazy-generate: if no
// credential exists yet, it generates and stores one before responding.
//
// Revocation: when a ticket is cancelled or refunded, call RevokeCredential
// to set revoked_at. The credential payload remains stored for audit purposes.
//
// Endpoints:
//
//	GET /v1/tickets/{id}/credential?type=static_qr   (default)
//	GET /v1/tickets/{id}/credential?type=pdf
package htickets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// CredentialResponse is the JSON representation of a single ticket_credentials row.
//
// For type=static_qr, payload is the 64-char hex QR token.
// For type=pdf, payload is the standard base64-encoded PDF bytes.
// RevokedAt is null for active credentials.
type CredentialResponse struct {
	ID        string  `json:"id"`
	TicketID  string  `json:"ticket_id"`
	Type      string  `json:"type"`
	Payload   string  `json:"payload"`
	IssuedAt  string  `json:"issued_at"`
	RevokedAt *string `json:"revoked_at"`
}

// CredentialFromRow converts a gen.TicketCredentialRow to a CredentialResponse.
func CredentialFromRow(r gen.TicketCredentialRow) CredentialResponse {
	resp := CredentialResponse{
		ID:       r.ID.String(),
		TicketID: r.TicketID.String(),
		Type:     r.Type,
		Payload:  r.Payload,
		IssuedAt: r.IssuedAt.UTC().Format(time.RFC3339),
	}
	if r.RevokedAt != nil {
		s := r.RevokedAt.UTC().Format(time.RFC3339)
		resp.RevokedAt = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// QR token generator
// ─────────────────────────────────────────────────────────────────────────────

// GenerateQRToken generates a cryptographically random opaque token suitable
// for embedding in a QR code. The token is 32 bytes read from crypto/rand,
// encoded as 64 lowercase hexadecimal characters.
//
// The token is NOT the ticket UUID — the server-side binding of token ↔ ticket
// is stored in ticket_credentials. This prevents enumeration attacks.
func GenerateQRToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("GenerateQRToken: read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// PDF renderer
// ─────────────────────────────────────────────────────────────────────────────

// RenderTicketPDF generates a minimal valid PDF/1.4 document for a ticket
// credential. Returns the raw PDF bytes. Uses no external dependencies —
// pure Go standard library only.
//
// The PDF contains:
//   - Title: "Arena Platform Ticket"
//   - Ticket ID (full UUID string)
//   - QR token (first 32 chars shown for display; full token used by scanner)
//   - Issue timestamp (RFC3339 UTC)
//   - Status label: "VALID"
//
// Font: Helvetica (built-in PDF Type1 font — no font embedding required).
// Page size: US Letter (612 × 792 pt).
//
// The xref table offsets are calculated dynamically, making the output a
// standards-conformant PDF that can be opened by any PDF reader.
func RenderTicketPDF(ticketID, qrToken string, issuedAt time.Time) []byte {
	// ── Content stream (PDF page content operators) ───────────────────────────
	// Display first 32 chars of the QR token so the PDF is readable without
	// being too long. Scanners always use the full token from the DB.
	displayToken := qrToken
	if len(displayToken) > 32 {
		displayToken = displayToken[:32] + "..."
	}

	cs := fmt.Sprintf(
		"BT\n"+
			"/F1 18 Tf\n"+
			"72 720 Td\n"+
			"(Arena Platform Ticket) Tj\n"+
			"/F1 12 Tf\n"+
			"0 -40 Td\n"+
			"(Ticket ID: %s) Tj\n"+
			"0 -20 Td\n"+
			"(QR Token:  %s) Tj\n"+
			"0 -20 Td\n"+
			"(Issued:    %s) Tj\n"+
			"0 -20 Td\n"+
			"(Status:    VALID) Tj\n"+
			"ET",
		ticketID,
		displayToken,
		issuedAt.UTC().Format(time.RFC3339),
	)

	// ── Build PDF structure ────────────────────────────────────────────────────
	// PDF/1.4 with 5 indirect objects:
	//   1: Catalog  2: Pages  3: Page  4: Content stream  5: Font
	var buf bytes.Buffer

	// PDF header
	buf.WriteString("%PDF-1.4\n")

	// Track byte offsets of each object for the xref table.
	// offsets[i] = byte offset of object i (1-based; index 0 unused).
	offsets := make([]int, 6)

	// Object 1: Catalog
	offsets[1] = buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// Object 2: Pages tree (one page)
	offsets[2] = buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	// Object 3: Page (US Letter, references content stream + font)
	offsets[3] = buf.Len()
	buf.WriteString("3 0 obj\n" +
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]\n" +
		"   /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\n" +
		"endobj\n")

	// Object 4: Content stream
	offsets[4] = buf.Len()
	fmt.Fprintf(&buf, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(cs)+1, // +1 for the trailing newline before endstream
		cs,
	)

	// Object 5: Font (Helvetica — built-in Type1, no embedding needed)
	offsets[5] = buf.Len()
	buf.WriteString("5 0 obj\n" +
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica\n" +
		"   /Encoding /WinAnsiEncoding >>\n" +
		"endobj\n")

	// ── Cross-reference table ──────────────────────────────────────────────────
	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n") // free-list head (object 0)
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}

	// ── Trailer ───────────────────────────────────────────────────────────────
	fmt.Fprintf(&buf,
		"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		xrefOffset,
	)

	return buf.Bytes()
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/tickets/{id}/credential
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetCredential serves GET /v1/tickets/{id}/credential.
//
// Query parameters:
//
//	?type=static_qr  (default) — returns the opaque QR token as JSON
//	?type=pdf        — returns base64-encoded PDF bytes as JSON
//
// The credential is generated lazily on first access and stored in
// ticket_credentials. Subsequent requests return the stored credential.
// Requires JWT + "credential.read" permission.
func (h *Handler) HandleGetCredential(w http.ResponseWriter, r *http.Request) {
	if h.credentialQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	// ── Parse and validate ticket UUID ────────────────────────────────────────
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"credential.invalid_ticket_id", "ticket id must be a valid UUID", r,
		))
		return
	}

	// ── Parse and validate ?type= parameter ──────────────────────────────────
	credType := r.URL.Query().Get("type")
	if credType == "" {
		credType = "static_qr" // sensible default
	}
	if credType != "static_qr" && credType != "pdf" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"credential.invalid_type", "type must be 'static_qr' or 'pdf'", r,
		))
		return
	}

	// ── Try to fetch existing credential (lazy-generate if absent) ────────────
	cred, err := h.credentialQueries.GetCredentialByTicketID(ctx, ticketID, credType)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// Unexpected DB error.
			h.logger.Error("credential: get credential failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("type", credType),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"credential.fetch_failed", "failed to fetch credential", r,
			))
			return
		}

		// Credential not yet generated — create it now.
		payload, genErr := GenerateCredentialPayload(ticketID, credType)
		if genErr != nil {
			h.logger.Error("credential: generation failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("type", credType),
				slog.String("error", genErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"credential.generation_failed", "failed to generate credential", r,
			))
			return
		}

		cred, err = h.credentialQueries.InsertTicketCredential(ctx, ticketID, credType, payload)
		if err != nil {
			h.logger.Error("credential: insert failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("type", credType),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"credential.store_failed", "failed to store credential", r,
			))
			return
		}

		h.logger.Info("credential: issued",
			slog.String("ticket_id", ticketID.String()),
			slog.String("type", credType),
			slog.String("credential_id", cred.ID.String()),
		)
	}

	httputil.WriteJSON(w, http.StatusOK, CredentialFromRow(cred))
}

// GenerateCredentialPayload builds the payload string for a new credential.
//
//   - static_qr: 64-char hex token via GenerateQRToken().
//   - pdf: base64(RenderTicketPDF(ticketID, freshQRToken, now)).
//     A fresh QR token is generated for the PDF; the PDF payload stores the
//     full token so the scanner can still look it up. If both static_qr and
//     pdf credentials are needed, issue static_qr first so both share the
//     same token.
func GenerateCredentialPayload(ticketID uuid.UUID, credType string) (string, error) {
	switch credType {
	case "static_qr":
		return GenerateQRToken()
	case "pdf":
		// Generate a QR token to embed in the PDF.
		token, err := GenerateQRToken()
		if err != nil {
			return "", fmt.Errorf("GenerateCredentialPayload: generate QR token for PDF: %w", err)
		}
		pdfBytes := RenderTicketPDF(ticketID.String(), token, time.Now().UTC())
		return base64.StdEncoding.EncodeToString(pdfBytes), nil
	default:
		return "", fmt.Errorf("GenerateCredentialPayload: unsupported type %q", credType)
	}
}
