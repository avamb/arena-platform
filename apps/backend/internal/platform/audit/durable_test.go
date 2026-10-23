package audit

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A route that promises "the audit row commits with the change" asks
// IsDurableInTx. Only the database writer may say yes, also through the two
// decorators the server wraps it in; a log-only writer, nil and a typed nil
// must all say no.
func TestIsDurableInTx(t *testing.T) {
	pg := NewPGWriter(&pgxpool.Pool{})
	cases := []struct {
		name string
		w    Writer
		want bool
	}{
		{"nil", nil, false},
		{"slog", NewSlogWriter(nil), false},
		{"typed nil pg writer", (*PGWriter)(nil), false},
		{"pg writer", pg, true},
		{"pg + service actor", WithServiceActor(pg), true},
		{"pg + via", WithVia(pg), true},
		{"the production stack", WithVia(WithServiceActor(pg)), true},
		{"slog + service actor", WithServiceActor(NewSlogWriter(nil)), false},
		{"slog + via", WithVia(NewSlogWriter(nil)), false},
		{"the production stack over slog", WithVia(WithServiceActor(NewSlogWriter(nil))), false},
	}
	for _, c := range cases {
		if got := IsDurableInTx(c.w); got != c.want {
			t.Errorf("%s: IsDurableInTx = %v, want %v", c.name, got, c.want)
		}
	}
}
