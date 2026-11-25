package hcheckout

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripClientSecrets_RemovesEverySecretAndKeepsTheRest(t *testing.T) {
	body := json.RawMessage(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_test_x","client_secret":"cs_test_x_secret_abc","ui_mode":"elements","payment_status":"paid","payment_intent":{"id":"pi_1","client_secret":"pi_1_secret_def"},"list":[{"client_secret":"z"}]}}}`)
	got := stripClientSecrets(body)
	if strings.Contains(string(got), "client_secret") || strings.Contains(string(got), "_secret_") {
		t.Fatalf("a client secret survived: %s", got)
	}
	var v struct {
		ID   string `json:"id"`
		Data struct {
			Object struct {
				ID            string `json:"id"`
				PaymentStatus string `json:"payment_status"`
				PaymentIntent struct {
					ID string `json:"id"`
				} `json:"payment_intent"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("scrubbed body is not JSON: %v", err)
	}
	if v.ID != "evt_1" || v.Data.Object.ID != "cs_test_x" || v.Data.Object.PaymentStatus != "paid" || v.Data.Object.PaymentIntent.ID != "pi_1" {
		t.Fatalf("scrubbed body lost other fields: %s", got)
	}
}

func TestStripClientSecrets_UntouchedWithoutSecretOrInvalid(t *testing.T) {
	plain := json.RawMessage(`{"id":"evt_2",  "data":{"object":{"id":"cs_2"}}}`)
	if got := stripClientSecrets(plain); string(got) != string(plain) {
		t.Fatalf("a body without a secret changed: %s", got)
	}
	bad := json.RawMessage(`not json`)
	if got := stripClientSecrets(bad); string(got) != string(bad) {
		t.Fatalf("invalid body changed: %s", got)
	}
	if got := stripClientSecrets(nil); got != nil {
		t.Fatalf("nil changed: %s", got)
	}
}
