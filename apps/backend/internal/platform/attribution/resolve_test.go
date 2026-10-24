package attribution

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type fakeLinks struct {
	rows  map[string]gen.SalesLinkRow
	err   error
	calls []string
}

func (f *fakeLinks) GetSalesLinkByCode(_ context.Context, code string) (gen.SalesLinkRow, error) {
	f.calls = append(f.calls, code)
	if f.err != nil {
		return gen.SalesLinkRow{}, f.err
	}
	r, ok := f.rows[code]
	if !ok {
		return gen.SalesLinkRow{}, pgx.ErrNoRows
	}
	return r, nil
}

func TestResolveLink(t *testing.T) {
	org := uuid.New()
	other := uuid.New()
	own := gen.SalesLinkRow{ID: uuid.New(), OrgID: org, Code: "abcdefg", Status: "active"}
	archived := gen.SalesLinkRow{ID: uuid.New(), OrgID: org, Code: "hjkmnpq", Status: "archived"}
	foreign := gen.SalesLinkRow{ID: uuid.New(), OrgID: other, Code: "rstuvwx"}
	q := &fakeLinks{rows: map[string]gen.SalesLinkRow{
		own.Code: own, archived.Code: archived, foreign.Code: foreign,
	}}
	ctx := context.Background()

	cases := []struct {
		name string
		ref  string
		want *uuid.UUID
	}{
		{"own link", "abcdefg", &own.ID},
		{"own link, case folded", "ABCDEFG", &own.ID},
		{"archived link still resolves", "hjkmnpq", &archived.ID},
		{"foreign link", "rstuvwx", nil},
		{"unknown code", "zzzzzzz", nil},
		{"no ref", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveLink(ctx, q, org, Attribution{Touch: Touch{Ref: c.ref}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch {
			case c.want == nil && got != nil:
				t.Fatalf("want nil, got %s", got)
			case c.want != nil && (got == nil || *got != *c.want):
				t.Fatalf("want %s, got %v", c.want, got)
			}
		})
	}

	t.Run("no ref makes no lookup", func(t *testing.T) {
		f := &fakeLinks{}
		if _, err := ResolveLink(ctx, f, org, Attribution{Touch: Touch{UTMSource: "qr"}}); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 0 {
			t.Fatalf("lookup made without a ref: %v", f.calls)
		}
	})

	t.Run("lookup error is returned", func(t *testing.T) {
		boom := errors.New("pool closed")
		got, err := ResolveLink(ctx, &fakeLinks{err: boom}, org, Attribution{Touch: Touch{Ref: "abcdefg"}})
		if !errors.Is(err, boom) || got != nil {
			t.Fatalf("want (nil, %v), got (%v, %v)", boom, got, err)
		}
	})

	t.Run("nil querier", func(t *testing.T) {
		got, err := ResolveLink(ctx, nil, org, Attribution{Touch: Touch{Ref: "abcdefg"}})
		if err != nil || got != nil {
			t.Fatalf("want (nil, nil), got (%v, %v)", got, err)
		}
	})
}
