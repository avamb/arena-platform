package hfeed

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type fakeFlagRow struct {
	val bool
	err error
}

func (r fakeFlagRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.val
	return nil
}

type fakeFlagDB struct{ row fakeFlagRow }

func (f fakeFlagDB) QueryRow(context.Context, string, ...any) pgx.Row { return f.row }

func TestOrgTrackingEnabled_Decision(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name string
		db   rowQuerier
		want bool
	}{
		{"nil db", nil, false},
		{"exists", fakeFlagDB{fakeFlagRow{val: true}}, true},
		{"none", fakeFlagDB{fakeFlagRow{val: false}}, false},
		{"read error says false", fakeFlagDB{fakeFlagRow{err: errors.New("boom")}}, false},
	}
	for _, c := range cases {
		if got := orgTrackingEnabled(context.Background(), c.db, nil, id); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
