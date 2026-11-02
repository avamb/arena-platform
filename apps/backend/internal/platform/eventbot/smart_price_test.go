package eventbot

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestParseSmartNums(t *testing.T) {
	cases := []struct {
		kind, text string
		want       smartNums
		ok         bool
	}{
		{"by_sales", "10 50 50", smartNums{Step: 10, Every: 50, Cap: 50}, true},
		{"by_days", " +10%, 7 , 50% ", smartNums{Step: 10, Every: 7, Cap: 50}, true},
		{"last_days", "20 3", smartNums{Step: 20, Every: 3}, true},
		{"last_days", "20 3 50", smartNums{}, false},
		{"by_sales", "10 50", smartNums{}, false},
		{"by_sales", "10 0 50", smartNums{}, false},
		{"by_sales", "ten 50 50", smartNums{}, false},
		{"by_sales", "10.5 50 50", smartNums{}, false},
	}
	for _, c := range cases {
		got, ok := parseSmartNums(c.kind, c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("parseSmartNums(%q, %q) = %+v, %v; want %+v, %v", c.kind, c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestSmartRequest_LastDaysSendsNoCap(t *testing.T) {
	id := uuid.New()
	if r := smartRequest("last_days", id, smartNums{Step: 20, Every: 3, Cap: 50}); r.CapPercent != nil {
		t.Errorf("last_days sent a cap: %v", *r.CapPercent)
	}
	r := smartRequest("by_sales", id, smartNums{Step: 10, Every: 30, Cap: 50})
	if r.CapPercent == nil || *r.CapPercent != 50 || r.TierIds[0] != id || r.Kind != "by_sales" {
		t.Errorf("by_sales request = %+v", r)
	}
}

func TestSmartRuleFor_FindsTheRuleOfAStep(t *testing.T) {
	head, step, other := uuid.New(), uuid.New(), uuid.New()
	rules := []openapi.SmartPriceRule{
		{Id: uuid.New(), TierId: head, Status: "overridden"},
		{Id: uuid.New(), TierId: head, StepTierIds: []uuid.UUID{step}, Status: "active"},
		{Id: uuid.New(), TierId: other, Status: "stopped"},
	}
	if a, e := smartRuleFor(rules, head); a == nil || a.Id != rules[1].Id || e == nil || e.Id != rules[0].Id {
		t.Errorf("head: active %v ended %v", a, e)
	}
	if a, e := smartRuleFor(rules, step); a == nil || a.Id != rules[1].Id || e != nil {
		t.Errorf("a later step must follow the head's preset: active %v ended %v", a, e)
	}
	if a, e := smartRuleFor(rules, other); a != nil || e == nil {
		t.Errorf("other: active %v ended %v", a, e)
	}
}

func TestSmartErrKey(t *testing.T) {
	if k := smartErrKey(errors.New("no code")); k != "" {
		t.Errorf("an error without a code mapped to %q", k)
	}
}
