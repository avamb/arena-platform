// Package wporders loads the correspondence of WordPress-era orders to the
// platform (spec 62 section 14.6, P-14): the operator exports, when WordPress
// is stopped, a CSV of
//
//	wc_order_id, allpay_order_id, platform_order_system_id, ticket_system_ids
//
// and cmd/arena-wp-orders-import stores it in wp_order_links. The package is
// the whole logic (parse, check against the platform, upsert) so it can be
// tested against a database; the command only wires flags.
//
// Idempotent: a row is keyed by (organization, WooCommerce order id); a row
// identical to the stored one is "unchanged" and is not written, so a second
// run of the same file changes nothing.
package wporders

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Mismatch codes.
const (
	CodeBadRow          = "bad_row"
	CodeDuplicate       = "duplicate_wc_order"
	CodeUnknownOrder    = "unknown_order"
	CodeUnknownTicket   = "unknown_ticket"
	CodeTicketNotInside = "ticket_not_in_order"
)

// Row is one line of the file.
type Row struct {
	Line            int
	WCOrderID       string
	AllPayOrderID   string
	SystemID        int64
	TicketSystemIDs []int64
}

// Mismatch is a line that cannot be stored, with the reason.
type Mismatch struct {
	Line      int
	WCOrderID string
	Code      string
	Detail    string
}

// Report is the outcome of a run (or a dry run).
type Report struct {
	Rows       int
	Inserted   int
	Updated    int
	Unchanged  int
	Mismatches []Mismatch
}

var requiredColumns = []string{"wc_order_id", "allpay_order_id", "platform_order_system_id", "ticket_system_ids"}

// Parse reads the CSV. A comma or a semicolon separates columns (the header
// line decides). Lines that cannot be read become mismatches; the readable
// ones are returned.
func Parse(r io.Reader) ([]Row, []Mismatch, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	text := strings.TrimPrefix(string(raw), string([]byte{0xEF, 0xBB, 0xBF}))
	comma := ','
	if nl := strings.IndexAny(text, "\r\n"); nl > 0 {
		head := text[:nl]
		if strings.Count(head, ";") > strings.Count(head, ",") {
			comma = ';'
		}
	}
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	head, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("wporders: the file has no header line: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, c := range requiredColumns {
		if _, ok := col[c]; !ok {
			return nil, nil, fmt.Errorf("wporders: column %q is missing (need %s)", c, strings.Join(requiredColumns, ", "))
		}
	}
	var rows []Row
	var bad []Mismatch
	seen := map[string]Row{}
	line := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			bad = append(bad, Mismatch{Line: line, Code: CodeBadRow, Detail: err.Error()})
			continue
		}
		if allEmpty(rec) {
			continue
		}
		get := func(name string) string {
			i := col[name]
			if i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		row := Row{Line: line, WCOrderID: get("wc_order_id"), AllPayOrderID: get("allpay_order_id")}
		if row.WCOrderID == "" || len(row.WCOrderID) > 64 {
			bad = append(bad, Mismatch{Line: line, WCOrderID: row.WCOrderID, Code: CodeBadRow, Detail: "wc_order_id is empty or longer than 64 characters"})
			continue
		}
		if len(row.AllPayOrderID) > 128 {
			bad = append(bad, Mismatch{Line: line, WCOrderID: row.WCOrderID, Code: CodeBadRow, Detail: "allpay_order_id is longer than 128 characters"})
			continue
		}
		sid, err := strconv.ParseInt(get("platform_order_system_id"), 10, 64)
		if err != nil || sid <= 0 {
			bad = append(bad, Mismatch{Line: line, WCOrderID: row.WCOrderID, Code: CodeBadRow, Detail: "platform_order_system_id is not a positive number"})
			continue
		}
		row.SystemID = sid
		ids, err := parseIDs(get("ticket_system_ids"))
		if err != nil {
			bad = append(bad, Mismatch{Line: line, WCOrderID: row.WCOrderID, Code: CodeBadRow, Detail: err.Error()})
			continue
		}
		row.TicketSystemIDs = ids
		if prev, dup := seen[row.WCOrderID]; dup {
			if !sameRow(prev, row) {
				bad = append(bad, Mismatch{Line: line, WCOrderID: row.WCOrderID, Code: CodeDuplicate,
					Detail: fmt.Sprintf("the order appears again with other data (first on line %d)", prev.Line)})
			}
			continue
		}
		seen[row.WCOrderID] = row
		rows = append(rows, row)
	}
	return rows, bad, nil
}

func allEmpty(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

func sameRow(a, b Row) bool {
	if a.AllPayOrderID != b.AllPayOrderID || a.SystemID != b.SystemID || len(a.TicketSystemIDs) != len(b.TicketSystemIDs) {
		return false
	}
	for i := range a.TicketSystemIDs {
		if a.TicketSystemIDs[i] != b.TicketSystemIDs[i] {
			return false
		}
	}
	return true
}

// parseIDs reads ticket ids separated by ; , | or spaces. Sorted, unique.
func parseIDs(s string) ([]int64, error) {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == '|' || r == ' ' || r == '\t' })
	set := map[int64]bool{}
	out := []int64{}
	for _, x := range f {
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("ticket_system_ids holds %q, which is not a positive number", x)
		}
		if !set[n] {
			set[n] = true
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// DB is the pool or transaction the import works through.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxBeginner is a pool: a DB that can open a transaction.
type TxBeginner interface {
	DB
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Import checks every row against the platform and, unless dryRun, stores the
// valid ones in one transaction. Rows that do not check out are reported and
// never stored; the caller decides whether that is fatal. parseProblems are
// the mismatches Parse already found, folded into the report.
func Import(ctx context.Context, db TxBeginner, orgID uuid.UUID, rows []Row, parseProblems []Mismatch, dryRun bool) (Report, error) {
	rep := Report{Rows: len(rows) + len(parseProblems), Mismatches: append([]Mismatch(nil), parseProblems...)}
	type resolved struct {
		row     Row
		orderID uuid.UUID
	}
	var ok []resolved
	for _, row := range rows {
		orderID, m, err := check(ctx, db, orgID, row)
		if err != nil {
			return rep, err
		}
		if m != nil {
			rep.Mismatches = append(rep.Mismatches, *m)
			continue
		}
		ok = append(ok, resolved{row, orderID})
	}
	sort.SliceStable(rep.Mismatches, func(i, j int) bool { return rep.Mismatches[i].Line < rep.Mismatches[j].Line })

	tx, err := db.Begin(ctx)
	if err != nil {
		return rep, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, r := range ok {
		var (
			allpay  *string
			orderID uuid.UUID
			tickets []int64
		)
		err := tx.QueryRow(ctx, `
			SELECT allpay_order_id, order_id, ticket_system_ids FROM wp_order_links
			WHERE org_id = $1 AND wc_order_id = $2`, orgID, r.row.WCOrderID).
			Scan(&allpay, &orderID, &tickets)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			rep.Inserted++
		case err != nil:
			return rep, err
		case same(allpay, orderID, tickets, r.row.AllPayOrderID, r.orderID, r.row.TicketSystemIDs):
			rep.Unchanged++
			continue
		default:
			rep.Updated++
		}
		if dryRun {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO wp_order_links (org_id, wc_order_id, allpay_order_id, order_id, ticket_system_ids)
			VALUES ($1, $2, NULLIF($3, ''), $4, $5)
			ON CONFLICT (org_id, wc_order_id) DO UPDATE SET
			    allpay_order_id = EXCLUDED.allpay_order_id,
			    order_id = EXCLUDED.order_id,
			    ticket_system_ids = EXCLUDED.ticket_system_ids,
			    imported_at = now()`,
			orgID, r.row.WCOrderID, r.row.AllPayOrderID, r.orderID, r.row.TicketSystemIDs); err != nil {
			return rep, fmt.Errorf("wc order %s: %w", r.row.WCOrderID, err)
		}
	}
	if dryRun {
		return rep, nil
	}
	return rep, tx.Commit(ctx)
}

func same(allpay *string, orderID uuid.UUID, tickets []int64, wantAllpay string, wantOrder uuid.UUID, want []int64) bool {
	have := ""
	if allpay != nil {
		have = *allpay
	}
	if have != wantAllpay || orderID != wantOrder || len(tickets) != len(want) {
		return false
	}
	t := append([]int64(nil), tickets...)
	sort.Slice(t, func(i, j int) bool { return t[i] < t[j] })
	for i := range t {
		if t[i] != want[i] {
			return false
		}
	}
	return true
}

// check resolves the platform order of a row and verifies its tickets.
func check(ctx context.Context, db DB, orgID uuid.UUID, row Row) (uuid.UUID, *Mismatch, error) {
	var orderID uuid.UUID
	err := db.QueryRow(ctx, `SELECT id FROM orders WHERE org_id = $1 AND system_id = $2`, orgID, row.SystemID).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, &Mismatch{Line: row.Line, WCOrderID: row.WCOrderID, Code: CodeUnknownOrder,
			Detail: fmt.Sprintf("no order with system_id %d in this organization", row.SystemID)}, nil
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	if len(row.TicketSystemIDs) == 0 {
		return orderID, nil, nil
	}
	inOrder := map[int64]bool{}
	rs, err := db.Query(ctx, `
		SELECT t.system_ticket_id FROM order_items oi JOIN tickets t ON t.id = oi.ticket_id
		WHERE oi.order_id = $1`, orderID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	for rs.Next() {
		var n int64
		if err := rs.Scan(&n); err != nil {
			rs.Close()
			return uuid.Nil, nil, err
		}
		inOrder[n] = true
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return uuid.Nil, nil, err
	}
	for _, id := range row.TicketSystemIDs {
		if inOrder[id] {
			continue
		}
		var exists bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tickets WHERE system_ticket_id = $1)`, id).Scan(&exists); err != nil {
			return uuid.Nil, nil, err
		}
		code := CodeTicketNotInside
		detail := fmt.Sprintf("ticket %d is not in order %d", id, row.SystemID)
		if !exists {
			code = CodeUnknownTicket
			detail = fmt.Sprintf("no ticket with system id %d", id)
		}
		return uuid.Nil, &Mismatch{Line: row.Line, WCOrderID: row.WCOrderID, Code: code, Detail: detail}, nil
	}
	return orderID, nil, nil
}
