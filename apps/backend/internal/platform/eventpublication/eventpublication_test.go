package eventpublication

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestChooseChannels(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	cases := []struct {
		name string
		in   []gen.BindableChannel
		want []uuid.UUID
	}{
		{"none", nil, nil},
		{"one, page off", []gen.BindableChannel{{ID: a}}, []uuid.UUID{a}},
		{"one, page on", []gen.BindableChannel{{ID: a, HostedPageEnabled: true}}, []uuid.UUID{a}},
		{"several, none on: the oldest", []gen.BindableChannel{{ID: a}, {ID: b}, {ID: c}}, []uuid.UUID{a}},
		{"several, one on", []gen.BindableChannel{{ID: a}, {ID: b, HostedPageEnabled: true}, {ID: c}}, []uuid.UUID{b}},
		{"several, two on: both, oldest first", []gen.BindableChannel{{ID: a}, {ID: b, HostedPageEnabled: true}, {ID: c, HostedPageEnabled: true}}, []uuid.UUID{b, c}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChooseChannels(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
