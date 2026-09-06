package compatids_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// fakeDBTX is a minimal in-memory stand-in for gen.DBTX that emulates the
// compatibility_id_map (kind, platform_id) and (kind, system_id) unique
// indexes and the compatibility_system_id_seq sequence. It only implements
// the four queries defined in compat_ids.sql.go — every other query panics
// so tests can never accidentally exercise unrelated code paths.
type fakeDBTX struct {
	nextSeq int64
	// key: kind + ":" + platform_id.String()
	byPlatform map[string]gen.CompatibilityIDRow
	// key: kind + ":" + system_id
	bySystem map[string]gen.CompatibilityIDRow
}

func newFake() *fakeDBTX {
	return &fakeDBTX{
		nextSeq:    1_000_000_000,
		byPlatform: map[string]gen.CompatibilityIDRow{},
		bySystem:   map[string]gen.CompatibilityIDRow{},
	}
}

// Exec implements the BulkInsertCompatibilityIDs branch: mint a fresh
// arena-owned row (via execEnsure's same insert semantics) for every
// platform id that does not already exist, silently skipping ones that do —
// mirroring the real query's ON CONFLICT DO NOTHING.
func (f *fakeDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if !containsTag(sql, "BulkInsertCompatibilityIDs") {
		panic("fakeDBTX: unrecognised exec: " + sql)
	}
	kind := args[0].(string)
	pids := args[1].([]uuid.UUID)
	for _, pid := range pids {
		if _, ok := f.byPlatform[kind+":"+pid.String()]; ok {
			continue
		}
		row := gen.CompatibilityIDRow{
			Kind:       kind,
			SystemID:   f.nextSeq,
			PlatformID: pid,
			Source:     "arena",
		}
		f.nextSeq++
		f.byPlatform[kind+":"+pid.String()] = row
		f.bySystem[kind+":"+itoa(row.SystemID)] = row
	}
	return pgconn.CommandTag{}, nil
}

// Query implements the ListCompatibilityIDsByPlatformIDs branch: return
// every already-registered row (kind, platform_id) pair matching the
// requested ids, in an arbitrary order — mirroring the real query's lack of
// an ORDER BY.
func (f *fakeDBTX) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if !containsTag(sql, "ListCompatibilityIDsByPlatformIDs") {
		panic("fakeDBTX: unrecognised query: " + sql)
	}
	kind := args[0].(string)
	pids := args[1].([]uuid.UUID)
	var rows []gen.CompatibilityIDRow
	for _, pid := range pids {
		if row, ok := f.byPlatform[kind+":"+pid.String()]; ok {
			rows = append(rows, row)
		}
	}
	return &fakeRows{rows: rows, idx: -1}, nil
}

// fakeRows is a minimal pgx.Rows implementation over an in-memory slice,
// backing fakeDBTX.Query above.
type fakeRows struct {
	rows []gen.CompatibilityIDRow
	idx  int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Next() bool {
	r.idx++
	return r.idx < len(r.rows)
}
func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.idx]
	*(dest[0].(*string)) = row.Kind
	*(dest[1].(*int64)) = row.SystemID
	*(dest[2].(*uuid.UUID)) = row.PlatformID
	*(dest[3].(*string)) = row.Source
	return nil
}
func (r *fakeRows) Values() ([]any, error) { panic("fakeRows: Values not implemented") }
func (r *fakeRows) RawValues() [][]byte    { panic("fakeRows: RawValues not implemented") }
func (r *fakeRows) Conn() *pgx.Conn        { return nil }

// QueryRow parses the query SQL text by prefix (the first line of the query
// carries the sqlc `-- name: X :one` tag) and dispatches to the right
// in-memory operation.
func (f *fakeDBTX) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch {
	case containsTag(sql, "EnsureCompatibilityID"):
		return f.execEnsure(args[0].(string), args[1].(uuid.UUID))
	case containsTag(sql, "GetCompatibilityIDByPlatformID"):
		return f.execGetByPlatform(args[0].(string), args[1].(uuid.UUID))
	case containsTag(sql, "GetCompatibilityIDBySystemID"):
		return f.execGetBySystem(args[0].(string), args[1].(int64))
	case containsTag(sql, "RegisterExternalCompatibilityID"):
		return f.execRegisterExternal(args[0].(string), args[1].(uuid.UUID), args[2].(int64))
	}
	panic("fakeDBTX: unrecognised query: " + sql)
}

func containsTag(sql, tag string) bool {
	// Every gen.* query embeds `-- name: <Name> :one` on the first line.
	needle := "-- name: " + tag + " "
	for i := 0; i+len(needle) <= len(sql); i++ {
		if sql[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func (f *fakeDBTX) execEnsure(kind string, pid uuid.UUID) pgx.Row {
	if _, ok := f.byPlatform[kind+":"+pid.String()]; ok {
		return errRow{err: pgx.ErrNoRows}
	}
	row := gen.CompatibilityIDRow{
		Kind:       kind,
		SystemID:   f.nextSeq,
		PlatformID: pid,
		Source:     "arena",
	}
	f.nextSeq++
	f.byPlatform[kind+":"+pid.String()] = row
	f.bySystem[kind+":"+itoa(row.SystemID)] = row
	return staticRow{row: row}
}

func (f *fakeDBTX) execGetByPlatform(kind string, pid uuid.UUID) pgx.Row {
	row, ok := f.byPlatform[kind+":"+pid.String()]
	if !ok {
		return errRow{err: pgx.ErrNoRows}
	}
	return staticRow{row: row}
}

func (f *fakeDBTX) execGetBySystem(kind string, sid int64) pgx.Row {
	row, ok := f.bySystem[kind+":"+itoa(sid)]
	if !ok {
		return errRow{err: pgx.ErrNoRows}
	}
	return staticRow{row: row}
}

func (f *fakeDBTX) execRegisterExternal(kind string, pid uuid.UUID, sid int64) pgx.Row {
	if _, ok := f.byPlatform[kind+":"+pid.String()]; ok {
		return errRow{err: pgx.ErrNoRows}
	}
	if _, ok := f.bySystem[kind+":"+itoa(sid)]; ok {
		return errRow{err: pgx.ErrNoRows}
	}
	row := gen.CompatibilityIDRow{
		Kind:       kind,
		SystemID:   sid,
		PlatformID: pid,
		Source:     "bil24",
	}
	f.byPlatform[kind+":"+pid.String()] = row
	f.bySystem[kind+":"+itoa(sid)] = row
	return staticRow{row: row}
}

func itoa(v int64) string {
	// Small helper to avoid importing strconv only for one call.
	// Positive int64 only — fake never sees negative sequence values.
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

type staticRow struct{ row gen.CompatibilityIDRow }

func (s staticRow) Scan(dest ...any) error {
	*(dest[0].(*string)) = s.row.Kind
	*(dest[1].(*int64)) = s.row.SystemID
	*(dest[2].(*uuid.UUID)) = s.row.PlatformID
	*(dest[3].(*string)) = s.row.Source
	// created_at zero-value is fine for tests.
	return nil
}

type errRow struct{ err error }

func (e errRow) Scan(_ ...any) error { return e.err }

// ─── tests ──────────────────────────────────────────────────────────────────

func TestValidateKind(t *testing.T) {
	for _, k := range compatids.AllKinds {
		if err := compatids.ValidateKind(k); err != nil {
			t.Errorf("ValidateKind(%q) unexpected error: %v", k, err)
		}
	}
	if err := compatids.ValidateKind("seat"); !errors.Is(err, compatids.ErrUnknownKind) {
		t.Errorf("ValidateKind(seat) = %v; want ErrUnknownKind", err)
	}
}

func TestEnsure_MintsThenReuses(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	pid := uuid.New()

	first, err := compatids.Ensure(ctx, db, compatids.KindAction, pid)
	if err != nil {
		t.Fatalf("Ensure first: %v", err)
	}
	if first < 1_000_000_000 {
		t.Errorf("Ensure first: id %d must be >= 1e9", first)
	}

	second, err := compatids.Ensure(ctx, db, compatids.KindAction, pid)
	if err != nil {
		t.Fatalf("Ensure second: %v", err)
	}
	if second != first {
		t.Errorf("Ensure second: got %d, want %d (idempotent)", second, first)
	}
}

func TestEnsure_RejectsNilAndUnknownKind(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	if _, err := compatids.Ensure(ctx, db, "seat", uuid.New()); !errors.Is(err, compatids.ErrUnknownKind) {
		t.Errorf("Ensure with unknown kind = %v; want ErrUnknownKind", err)
	}
	if _, err := compatids.Ensure(ctx, db, compatids.KindAction, uuid.Nil); err == nil {
		t.Errorf("Ensure with nil platformID: expected error")
	}
}

func TestEnsureMany_DedupesAndReturnsAll(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	a, b := uuid.New(), uuid.New()

	out, err := compatids.EnsureMany(ctx, db, compatids.KindActionEvent, []uuid.UUID{a, b, a})
	if err != nil {
		t.Fatalf("EnsureMany: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("EnsureMany: got %d entries, want 2", len(out))
	}
	if out[a] == 0 || out[b] == 0 || out[a] == out[b] {
		t.Errorf("EnsureMany: expected distinct positive ids, got a=%d b=%d", out[a], out[b])
	}
}

func TestResolve_UnknownReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	db := newFake()

	if _, err := compatids.Resolve(ctx, db, compatids.KindVenue, 999); !errors.Is(err, compatids.ErrNotFound) {
		t.Errorf("Resolve unknown = %v; want ErrNotFound", err)
	}
}

func TestResolve_ReturnsPlatformID(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	pid := uuid.New()

	sid, err := compatids.Ensure(ctx, db, compatids.KindVenue, pid)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := compatids.Resolve(ctx, db, compatids.KindVenue, sid)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != pid {
		t.Errorf("Resolve: got %s, want %s", got, pid)
	}
}

func TestRegisterExternal_RejectsCeiling(t *testing.T) {
	ctx := context.Background()
	db := newFake()

	err := compatids.RegisterExternal(ctx, db, compatids.KindAction, uuid.New(), 1_000_000_000)
	if !errors.Is(err, compatids.ErrExternalIDOutOfRange) {
		t.Errorf("RegisterExternal at ceiling: got %v; want ErrExternalIDOutOfRange", err)
	}
	err = compatids.RegisterExternal(ctx, db, compatids.KindAction, uuid.New(), 2_500_000_000)
	if !errors.Is(err, compatids.ErrExternalIDOutOfRange) {
		t.Errorf("RegisterExternal above ceiling: got %v; want ErrExternalIDOutOfRange", err)
	}
}

func TestRegisterExternal_RejectsNonPositive(t *testing.T) {
	ctx := context.Background()
	db := newFake()

	if err := compatids.RegisterExternal(ctx, db, compatids.KindAction, uuid.New(), 0); err == nil {
		t.Errorf("RegisterExternal(0): expected error")
	}
	if err := compatids.RegisterExternal(ctx, db, compatids.KindAction, uuid.New(), -1); err == nil {
		t.Errorf("RegisterExternal(-1): expected error")
	}
}

func TestRegisterExternal_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	pid := uuid.New()

	if err := compatids.RegisterExternal(ctx, db, compatids.KindCity, pid, 42); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := compatids.RegisterExternal(ctx, db, compatids.KindCity, pid, 42); err != nil {
		t.Errorf("second register (idempotent): %v; want nil", err)
	}
}

func TestRegisterExternal_Collision(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	pid1, pid2 := uuid.New(), uuid.New()

	if err := compatids.RegisterExternal(ctx, db, compatids.KindCity, pid1, 100); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := compatids.RegisterExternal(ctx, db, compatids.KindCity, pid2, 100)
	if !errors.Is(err, compatids.ErrExternalIDCollision) {
		t.Errorf("collision: got %v; want ErrExternalIDCollision", err)
	}
}

func TestRegisterExternal_ConflictSamePlatformDifferentSID(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	pid := uuid.New()

	if err := compatids.RegisterExternal(ctx, db, compatids.KindCity, pid, 100); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := compatids.RegisterExternal(ctx, db, compatids.KindCity, pid, 200)
	if !errors.Is(err, compatids.ErrExternalIDConflict) {
		t.Errorf("conflict: got %v; want ErrExternalIDConflict", err)
	}
}

func TestEnsureAfterRegisterExternal_UsesExternalID(t *testing.T) {
	ctx := context.Background()
	db := newFake()
	pid := uuid.New()

	if err := compatids.RegisterExternal(ctx, db, compatids.KindAction, pid, 777); err != nil {
		t.Fatalf("register external: %v", err)
	}
	// Ensure on the same platform_id should NOT mint a new id — it should
	// return the externally-registered value (< 1e9).
	got, err := compatids.Ensure(ctx, db, compatids.KindAction, pid)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != 777 {
		t.Errorf("Ensure after RegisterExternal: got %d; want 777", got)
	}
}
