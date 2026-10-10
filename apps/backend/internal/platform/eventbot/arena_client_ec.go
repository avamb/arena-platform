package eventbot

// arena_client_ec.go — the calls behind the event-center screens of spec 35
// stage 1: the event summary (EC-03/06) and the CSV exports (EC-07). The
// summary is a plain JSON read; an export is a file, so it is downloaded with
// Accept: text/csv and handed to Telegram as a document by the caller. The
// bytes of an export hold buyers' names, e-mails and phones: nothing here
// logs them, and the only place they go is the requesting private chat.

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// csvMaxBytes bounds the file the bot forwards: the server stops at 50 000
// rows, which is far below Telegram's 50 MB bot limit, and spec 35 §4.3 sets
// 20 MB as the line above which the screen asks to narrow the selection.
const csvMaxBytes = 20 << 20

// errCSVTooLarge is what DownloadCSV answers for a file over csvMaxBytes; the
// screen treats it exactly like the server's 413 export.too_many_rows.
var errCSVTooLarge = fmt.Errorf("eventbot: export larger than %d bytes", csvMaxBytes)

// CSVFile is a downloaded export.
type CSVFile struct {
	// Name is the file name the API proposed (Content-Disposition), made safe.
	Name string
	Body []byte
	// Rows counts the data rows (the header is not one).
	Rows int
}

// EventSummary returns the totals of an event over all its sessions plus the
// per-session figures (GET .../events/{event_id}/summary).
func (c *ArenaClient) EventSummary(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (openapi.EventSummary, error) {
	var out openapi.EventSummary
	path := "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/summary"
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// SessionSalesCSV downloads one ticket row per sale of a session.
func (c *ArenaClient) SessionSalesCSV(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, locale string) (CSVFile, error) {
	return c.DownloadCSV(ctx, jwt, "/v1/organizations/"+orgID.String()+"/sessions/"+sessionID.String()+"/sales.csv", locale)
}

// EventSalesCSV downloads the same rows over every session of an event.
func (c *ArenaClient) EventSalesCSV(ctx context.Context, jwt string, orgID, eventID uuid.UUID, locale string) (CSVFile, error) {
	return c.DownloadCSV(ctx, jwt, "/v1/organizations/"+orgID.String()+"/events/"+eventID.String()+"/sales.csv", locale)
}

// SessionSummaryCSV downloads the per-category summary of a session.
func (c *ArenaClient) SessionSummaryCSV(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, locale string) (CSVFile, error) {
	return c.DownloadCSV(ctx, jwt, "/v1/organizations/"+orgID.String()+"/sessions/"+sessionID.String()+"/summary.csv", locale)
}

// DownloadCSV fetches an export route. locale picks the language of the
// column headers (the API falls back to English for an unknown tag). A
// non-2xx answer is an *APIError — 413 export.too_many_rows when the file
// would exceed the server's row cap, 404 for a row of another organization.
func (c *ArenaClient) DownloadCSV(ctx context.Context, jwt, path, locale string) (CSVFile, error) {
	if locale != "" {
		path += "?locale=" + url.QueryEscape(locale)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return CSVFile{}, err
	}
	req.Header.Set("Accept", "text/csv")
	req.Header.Set("X-Admin-Reason", AdminReason)
	req.Header.Set(audit.HeaderClientChannel, ClientChannel)
	req.Header.Set("Authorization", "Bearer "+jwt)
	res, err := c.http.Do(req)
	if err != nil {
		// The URL carries ids only, but the wrapped error is logged by the
		// caller: keep it to the verb and the route.
		return CSVFile{}, fmt.Errorf("GET export: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, csvMaxBytes+1))
	if err != nil {
		return CSVFile{}, fmt.Errorf("read export: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return CSVFile{}, apiErrorFrom(res.StatusCode, raw)
	}
	if len(raw) > csvMaxBytes {
		return CSVFile{}, errCSVTooLarge
	}
	return CSVFile{
		Name: safeFileName(res.Header.Get("Content-Disposition")),
		Body: raw,
		Rows: CSVRows(raw),
	}, nil
}

// apiErrorFrom builds the APIError of a non-2xx answer whose body is the
// JSON error envelope (or anything else, which only costs the message).
func apiErrorFrom(status int, raw []byte) *APIError {
	ae := &APIError{Status: status, Code: "http." + http.StatusText(status)}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if decodeJSON(raw, &env) == nil && env.Error.Code != "" {
		ae.Code, ae.Message = env.Error.Code, env.Error.Message
	}
	return ae
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeFileName takes the filename of a Content-Disposition header and keeps
// only characters that are safe in a document name; the fallback is
// "export.csv".
func safeFileName(disposition string) string {
	name := ""
	if _, params, err := mime.ParseMediaType(disposition); err == nil {
		name = params["filename"]
	}
	if strings.HasSuffix(strings.ToLower(name), ".csv") {
		name = name[:len(name)-len(".csv")]
	}
	name = strings.Trim(unsafeFileChars.ReplaceAllString(name, "_"), "._-")
	if name == "" {
		name = "export"
	}
	return name + ".csv"
}

// CSVRows counts the data rows of an export: records end with a newline that
// is NOT inside quotes (a buyer's name may hold a line break), the first
// record is the header. A last record without a final newline still counts.
func CSVRows(b []byte) int {
	records, inQuotes, lineHasData := 0, false, false
	for _, c := range b {
		switch c {
		case '"':
			inQuotes = !inQuotes
			lineHasData = true
		case '\n':
			if inQuotes {
				lineHasData = true
				continue
			}
			if lineHasData {
				records++
			}
			lineHasData = false
		case '\r':
		default:
			lineHasData = true
		}
	}
	if lineHasData {
		records++
	}
	if records > 0 {
		records-- // the header
	}
	return records
}
