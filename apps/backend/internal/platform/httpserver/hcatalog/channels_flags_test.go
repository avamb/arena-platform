package hcatalog

import (
	"encoding/json"
	"strings"
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

func TestUpdateChannelRequest_PaymentWindowIsTriState(t *testing.T) {
	var absent, cleared, set updateChannelRequest
	_ = json.Unmarshal([]byte(`{"name":"x"}`), &absent)
	_ = json.Unmarshal([]byte(`{"payment_window_seconds":null}`), &cleared)
	_ = json.Unmarshal([]byte(`{"payment_window_seconds":1200}`), &set)
	if absent.PaymentWindowSeconds.Present {
		t.Fatal("an absent key must keep the stored window")
	}
	if !cleared.PaymentWindowSeconds.Present || cleared.PaymentWindowSeconds.Value != nil {
		t.Fatal("null must clear the window to the deployment default")
	}
	if !set.PaymentWindowSeconds.Present || set.PaymentWindowSeconds.Value == nil || *set.PaymentWindowSeconds.Value != 1200 {
		t.Fatal("a number must set the window")
	}
}

func TestValidatePaymentWindowSeconds(t *testing.T) {
	for _, v := range []int32{119, 0, -5, 86401} {
		v := v
		if ValidatePaymentWindowSeconds(&v) == "" {
			t.Errorf("%d seconds must be refused", v)
		}
	}
	for _, v := range []int32{120, 1200, 86400} {
		v := v
		if msg := ValidatePaymentWindowSeconds(&v); msg != "" {
			t.Errorf("%d seconds refused: %s", v, msg)
		}
	}
	if ValidatePaymentWindowSeconds(nil) != "" {
		t.Error("nil (the default) is always valid")
	}
	b, _ := json.Marshal(ChannelFromRow(gen.SalesChannelRow{}))
	if !strings.Contains(string(b), `"payment_window_seconds":null`) {
		t.Errorf("the channel response must carry payment_window_seconds: %s", b)
	}
}
