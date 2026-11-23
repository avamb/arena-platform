package bil24compat

import (
	"encoding/json"
	"testing"
)

func TestParseExternalSale(t *testing.T) {
	var ae ImportSessionActionEvent
	if k, _, err := ae.ParseExternalSale(); err != nil || k != ExternalSaleAbsent {
		t.Fatalf("absent: %v %v", k, err)
	}
	if err := json.Unmarshal([]byte(`{"externalSale":null}`), &ae); err != nil {
		t.Fatal(err)
	}
	if k, _, err := ae.ParseExternalSale(); err != nil || k != ExternalSaleNull {
		t.Fatalf("null: %v %v", k, err)
	}
	if err := json.Unmarshal([]byte(`{"externalSale":{"url":"https://a.example.com/x","sellerName":"A","priceFrom":12.5,"currency":"EUR"}}`), &ae); err != nil {
		t.Fatal(err)
	}
	k, s, err := ae.ParseExternalSale()
	if err != nil || k != ExternalSaleObject || s.URL != "https://a.example.com/x" || s.PriceFrom == nil || *s.PriceFrom != 12.5 {
		t.Fatalf("object: %v %+v %v", k, s, err)
	}
	ae.ExternalSale = json.RawMessage(`"x"`)
	if _, _, err := ae.ParseExternalSale(); err == nil {
		t.Fatal("a string is not an externalSale")
	}
	// An absent key stays out of the JSON (a platform session reads back unchanged).
	out, _ := json.Marshal(ImportSessionActionEvent{})
	if string(out) != "" && json.Valid(out) {
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		if _, has := m["externalSale"]; has {
			t.Fatalf("absent externalSale was marshalled: %s", out)
		}
	}
}
