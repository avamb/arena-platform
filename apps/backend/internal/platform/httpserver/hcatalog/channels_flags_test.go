package hcatalog

import (
	"encoding/json"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestChannelFromRow_CarriesFlags(t *testing.T) {
	b, err := json.Marshal(ChannelFromRow(gen.SalesChannelRow{IsTest: true, CollectName: true}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["is_test"] != true || m["collect_name"] != true || m["collect_phone"] != false {
		t.Fatalf("flags not in the channel response: %s", b)
	}
}

func TestUpdateChannelRequest_FlagsAreOptional(t *testing.T) {
	var req updateChannelRequest
	if err := json.Unmarshal([]byte(`{"collect_phone":false}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.IsTest != nil || req.CollectName != nil {
		t.Fatal("an absent key must stay nil (leave unchanged)")
	}
	if req.CollectPhone == nil || *req.CollectPhone {
		t.Fatal("an explicit false must be kept as false, not as absent")
	}
}
