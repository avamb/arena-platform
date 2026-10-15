package hmacsscan

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// ScanInput is one scan as MACS reports it. A scan names its ticket by
// SystemTicketID (the `id` of the MACS export, tickets.system_ticket_id),
// by Barcode (the 13-digit EAN-13 of the export), or by both, which must
// then point at the same ticket.
type ScanInput struct {
	ScanID         string `json:"scan_id"`
	SystemTicketID *int64 `json:"system_ticket_id"`
	Barcode        string `json:"barcode"`
	Result         string `json:"result"`
	ScannedAt      string `json:"scanned_at"`
	ScannerID      string `json:"scanner_id"`
	ScannerName    string `json:"scanner_name"`
	Gate           string `json:"gate"`
}

// Per-scan status values.
const (
	StatusAccepted  = "accepted"
	StatusDuplicate = "duplicate"
	StatusRejected  = "rejected"
)

// MACS verdicts. A refusal at the door (rejected, duplicate) is stored as
// scan_events.result 'denied'; the MACS word travels in the outbox payload.
const (
	ResultAdmitted  = "admitted"
	ResultRejected  = "rejected"
	ResultDuplicate = "duplicate"
)

// Per-scan error codes.
const (
	CodeInvalidScanID    = "scan.invalid_scan_id"
	CodeInvalidScannedAt = "scan.invalid_scanned_at"
	CodeScannedInFuture  = "scan.scanned_at_in_future"
	CodeInvalidResult    = "scan.invalid_result"
	CodeTicketRequired   = "scan.ticket_required"
	CodeInvalidTicketID  = "scan.invalid_system_ticket_id"
	CodeInvalidBarcode   = "scan.invalid_barcode"
	CodeFieldTooLong     = "scan.field_too_long"
	// CodeInvalidField is a value the database can never store (a NUL
	// character in a text field, or another data error): sending it again
	// can never succeed, so it is never retryable.
	CodeInvalidField   = "scan.invalid_field"
	CodeTicketNotFound = "scan.ticket_not_found"
	CodeTicketMismatch = "scan.ticket_mismatch"
	CodeUnavailable    = "scan.temporarily_unavailable"
)

// Field limits.
const (
	maxScanIDLen = 128
	maxLabelLen  = 200
	// futureSkew tolerates a scanner clock running a little ahead.
	futureSkew = 5 * time.Minute
)

// ScanResult is the outcome of one scan, in request order.
type ScanResult struct {
	ScanID         string  `json:"scan_id"`
	Status         string  `json:"status"`
	ScanEventID    string  `json:"scan_event_id,omitempty"`
	TicketID       *string `json:"ticket_id"`
	FirstAdmission bool    `json:"first_admission"`
	ErrorCode      string  `json:"error_code,omitempty"`
	Error          string  `json:"error,omitempty"`
	Retryable      bool    `json:"retryable,omitempty"`
}

func rejected(scanID, code, msg string) ScanResult {
	return ScanResult{ScanID: scanID, Status: StatusRejected, ErrorCode: code, Error: msg}
}

// scan is a validated ScanInput.
type scan struct {
	in        ScanInput
	scannedAt time.Time
	// storedResult is scan_events.result: admitted or denied.
	storedResult string
	barcode      string
	systemID     int64
	hasSystemID  bool
}

// credentialCode is what scan_events.credential_code records: the barcode
// MACS read, else the ticket id it named.
func (s scan) credentialCode() string {
	if s.barcode != "" {
		return s.barcode
	}
	return "system_ticket_id:" + strconv.FormatInt(s.systemID, 10)
}

// validate checks one scan without the database. ok false carries the
// rejection.
func validate(in ScanInput, now time.Time) (scan, ScanResult, bool) {
	id := strings.TrimSpace(in.ScanID)
	if id == "" || utf8.RuneCountInString(id) > maxScanIDLen || strings.ContainsRune(id, 0) {
		return scan{}, rejected(in.ScanID, CodeInvalidScanID, "scan_id is required, at most 128 characters, without NUL"), false
	}
	in.ScanID = id
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(in.ScannedAt))
	if err != nil {
		return scan{}, rejected(id, CodeInvalidScannedAt, "scanned_at must be RFC 3339 with a zone"), false
	}
	if at.After(now.Add(futureSkew)) {
		return scan{}, rejected(id, CodeScannedInFuture, "scanned_at lies in the future; check the scanner clock"), false
	}
	s := scan{in: in, scannedAt: at.UTC()}
	switch strings.TrimSpace(in.Result) {
	case ResultAdmitted:
		s.storedResult = "admitted"
	case ResultRejected, ResultDuplicate:
		s.storedResult = "denied"
	default:
		return scan{}, rejected(id, CodeInvalidResult, `result must be "admitted", "rejected" or "duplicate"`), false
	}
	s.in.Result = strings.TrimSpace(in.Result)
	for _, f := range []string{in.ScannerID, in.ScannerName, in.Gate} {
		if utf8.RuneCountInString(f) > maxLabelLen {
			return scan{}, rejected(id, CodeFieldTooLong, "scanner_id, scanner_name and gate are at most 200 characters"), false
		}
		// PostgreSQL text cannot hold NUL: such a value would fail the
		// INSERT on every attempt and keep the batch retryable forever.
		if strings.ContainsRune(f, 0) {
			return scan{}, rejected(id, CodeInvalidField, "scanner_id, scanner_name and gate must not contain NUL"), false
		}
	}
	if in.SystemTicketID != nil {
		if *in.SystemTicketID <= 0 {
			return scan{}, rejected(id, CodeInvalidTicketID, "system_ticket_id must be a positive integer"), false
		}
		s.systemID, s.hasSystemID = *in.SystemTicketID, true
	}
	if b := strings.TrimSpace(in.Barcode); b != "" {
		if !ean13.Valid(b) {
			return scan{}, rejected(id, CodeInvalidBarcode, "barcode must be a 13-digit EAN-13 with its check digit"), false
		}
		s.barcode = b
	}
	if !s.hasSystemID && s.barcode == "" {
		return scan{}, rejected(id, CodeTicketRequired, "name the ticket by system_ticket_id or barcode"), false
	}
	return s, ScanResult{}, true
}

// legacySystemID decodes a code of the retired ean13.PlatformCode formula
// ("21" + zero-padded system_ticket_id + check digit) back to the ticket id:
// the MACS export prints exactly that for a ticket without a stored EAN-13.
func legacySystemID(code string) (int64, bool) {
	if len(code) != 13 || !strings.HasPrefix(code, ean13.PlatformPrefix) || !ean13.Valid(code) {
		return 0, false
	}
	id, err := strconv.ParseInt(code[len(ean13.PlatformPrefix):12], 10, 64)
	if err != nil || id <= 0 || ean13.PlatformCode(id) != code {
		return 0, false
	}
	return id, true
}

// VerifySignature reports whether header is "sha256=<hex>" of the
// HMAC-SHA256 of body under secret, the scheme arena signs its own MACS
// webhooks with (macs/dispatcher.go). The comparison is constant time.
func VerifySignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	header = strings.TrimSpace(header)
	if secret == "" || len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	return hmac.Equal(got, Sign(secret, body))
}

// Sign is the raw HMAC-SHA256 of body under secret.
func Sign(secret string, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return mac.Sum(nil)
}

// SignatureHeader is the X-MACS-Signature value for body.
func SignatureHeader(secret string, body []byte) string {
	return "sha256=" + hex.EncodeToString(Sign(secret, body))
}
