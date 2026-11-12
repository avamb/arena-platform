package hgetstarted

import (
	"reflect"
	"testing"
)

func holds(perms ...string) func(string) bool {
	set := map[string]bool{}
	for _, p := range perms {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

func TestGettingStarted_Decide(t *testing.T) {
	all := []string{PermOrgData, PermPayments, PermEvent, PermSalesLink}
	cases := []struct {
		name     string
		perms    []string
		facts    Facts
		steps    []Step
		complete bool
	}{
		{
			name:  "owner of a fresh organization",
			perms: all,
			steps: []Step{{StepOrgData, false}, {StepPayments, false}, {StepEvent, false}, {StepSalesLink, false}},
		},
		{
			name:     "owner with everything done",
			perms:    all,
			facts:    Facts{OrgData: true, Payments: true, Event: true, SalesLink: true},
			steps:    []Step{{StepOrgData, true}, {StepPayments, true}, {StepEvent, true}, {StepSalesLink, true}},
			complete: true,
		},
		{
			name:  "owner with one step left",
			perms: all,
			facts: Facts{OrgData: true, Payments: true, Event: true},
			steps: []Step{{StepOrgData, true}, {StepPayments, true}, {StepEvent, true}, {StepSalesLink, false}},
		},
		{
			name:     "manager: unlisted steps do not hold complete back",
			perms:    []string{PermEvent, PermSalesLink, "promo.read"},
			facts:    Facts{Event: true, SalesLink: true},
			steps:    []Step{{StepEvent, true}, {StepSalesLink, true}},
			complete: true,
		},
		{
			name:  "fixed order whatever the permission order",
			perms: []string{PermSalesLink, PermOrgData},
			facts: Facts{SalesLink: true},
			steps: []Step{{StepOrgData, false}, {StepSalesLink, true}},
		},
		{
			name:     "no step listed is complete",
			perms:    []string{"event.read"},
			facts:    Facts{},
			steps:    []Step{},
			complete: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(holds(tc.perms...), tc.facts)
			if got.Steps == nil {
				t.Fatal("steps is nil; want an empty list")
			}
			if !reflect.DeepEqual(got.Steps, tc.steps) || got.Complete != tc.complete {
				t.Errorf("got %+v; want steps %+v complete %v", got, tc.steps, tc.complete)
			}
		})
	}
}
