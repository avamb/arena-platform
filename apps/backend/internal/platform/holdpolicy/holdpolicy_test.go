package holdpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestDefault_IsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
}

func TestValidate_MirrorsChecks(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Policy)
		ok   bool
	}{
		{"select min", func(p *Policy) { p.Select = 120 * time.Second }, true},
		{"select below", func(p *Policy) { p.Select = 119 * time.Second }, false},
		{"select max", func(p *Policy) { p.Select = 1800 * time.Second }, true},
		{"select above", func(p *Policy) { p.Select = 1801 * time.Second }, false},
		{"select fraction", func(p *Policy) { p.Select = 200*time.Second + time.Millisecond }, false},
		{"payment min", func(p *Policy) { p.Payment = 300 * time.Second }, true},
		{"payment below", func(p *Policy) { p.Payment = 299 * time.Second }, false},
		{"payment above", func(p *Policy) { p.Payment = 1801 * time.Second }, false},
		{"extend negative", func(p *Policy) { p.Extend = -time.Second }, false},
		{"extend_count 0", func(p *Policy) { p.ExtendCount = 0 }, true},
		{"extend_count 3", func(p *Policy) { p.ExtendCount = 3 }, true},
		{"extend_count 4", func(p *Policy) { p.ExtendCount = 4 }, false},
		{"extend_count -1", func(p *Policy) { p.ExtendCount = -1 }, false},
		{"paying negative", func(p *Policy) { p.Paying = -time.Second }, false},
		{"max seats 0", func(p *Policy) { p.MaxSeatsPerHold = 0 }, false},
		{"max seats at cart limit", func(p *Policy) { p.MaxSeatsPerHold = MaxCartSeats }, true},
		{"max seats over cart limit", func(p *Policy) { p.MaxSeatsPerHold = MaxCartSeats + 1 }, false},
		{"max device 0", func(p *Policy) { p.MaxActivePerDevice = 0 }, false},
		{"max ip 0", func(p *Policy) { p.MaxActivePerIP = 0 }, false},
		{"rehold negative", func(p *Policy) { p.ReholdPause = -time.Second }, false},
		{"poll min", func(p *Policy) { p.PollInterval = time.Second }, true},
		{"poll below", func(p *Policy) { p.PollInterval = 999 * time.Millisecond }, false},
		{"poll max", func(p *Policy) { p.PollInterval = 10 * time.Second }, true},
		{"poll above", func(p *Policy) { p.PollInterval = 10001 * time.Millisecond }, false},
	}
	for _, c := range cases {
		p := Default()
		c.mut(&p)
		err := p.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: Validate() = %v; want nil", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate() = %v; want ErrInvalid", c.name, err)
		}
	}
}

func TestPolicy_JSONRoundTrip(t *testing.T) {
	p := Default()
	p.Profile = ProfileHighDemand
	p.Select = 420 * time.Second
	p.PollInterval = 1500 * time.Millisecond
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["select_seconds"] != float64(420) || raw["poll_interval_ms"] != float64(1500) || raw["profile"] != ProfileHighDemand {
		t.Fatalf("snapshot JSON = %s", b)
	}
	var back Policy
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != p {
		t.Fatalf("round trip = %+v; want %+v", back, p)
	}
}

type fakeQ struct {
	row gen.HoldPolicyProfile
	err error
}

func (f fakeQ) GetSessionHoldPolicyProfile(context.Context, uuid.UUID) (gen.HoldPolicyProfile, error) {
	return f.row, f.err
}

func TestForSession_ErrorGivesDefault(t *testing.T) {
	p, err := ForSession(context.Background(), fakeQ{err: pgx.ErrNoRows}, uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) || p != Default() {
		t.Fatalf("ForSession = %+v, %v", p, err)
	}
	p, err = ForSession(context.Background(), fakeQ{row: gen.HoldPolicyProfile{Name: "x"}}, uuid.New())
	if !errors.Is(err, ErrInvalid) || p != Default() {
		t.Fatalf("invalid row: ForSession = %+v, %v", p, err)
	}
}
