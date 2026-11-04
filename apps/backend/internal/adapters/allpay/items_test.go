package allpay

import (
	"encoding/json"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func itemFields(t *testing.T, items []any) []map[string]string {
	t.Helper()
	out := make([]map[string]string, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("item is %T", it)
		}
		row := map[string]string{}
		for k, v := range m {
			switch x := v.(type) {
			case string:
				row[k] = x
			case json.Number:
				row[k] = x.String()
			default:
				t.Fatalf("field %s is %T", k, v)
			}
		}
		out = append(out, row)
	}
	return out
}

func TestCheckoutItems_OneLineByDefault(t *testing.T) {
	got := itemFields(t, checkoutItems("Einat Winery, order 7", 25000, nil, VATFull))
	if len(got) != 1 || got[0]["name"] != "Einat Winery, order 7" || got[0]["price"] != "250" || got[0]["qty"] != "1" || got[0]["vat"] != VATFull {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckoutItems_GoodsAndDeliveryAreTwoLinesWithTheSameVAT(t *testing.T) {
	lines := []payments.HostedCheckoutLine{{Name: "Einat Winery, order 7", Amount: 20000}, {Name: "Delivery", Amount: 5000}}
	got := itemFields(t, checkoutItems("Einat Winery, order 7", 25000, lines, VATFull))
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0]["name"] != "Einat Winery, order 7" || got[0]["price"] != "200" {
		t.Errorf("goods line %+v", got[0])
	}
	if got[1]["name"] != "Delivery" || got[1]["price"] != "50" {
		t.Errorf("delivery line %+v", got[1])
	}
	for _, it := range got {
		if it["vat"] != VATFull || it["qty"] != "1" {
			t.Errorf("line %+v: want qty 1 and vat %s", it, VATFull)
		}
	}
}

func TestCheckoutItems_LinesThatDoNotAddUpAreIgnored(t *testing.T) {
	for _, lines := range [][]payments.HostedCheckoutLine{
		{{Name: "Goods", Amount: 20000}, {Name: "Delivery", Amount: 4000}}, // 240 of 250
		{{Name: "Goods", Amount: 25000}, {Name: "Delivery", Amount: 0}},    // an empty line
		{{Name: " ", Amount: 20000}, {Name: "Delivery", Amount: 5000}},     // a nameless line
	} {
		got := itemFields(t, checkoutItems("Order 7", 25000, lines, VATNone))
		if len(got) != 1 || got[0]["price"] != "250" || got[0]["name"] != "Order 7" {
			t.Errorf("lines %+v: got %+v want one 250 line", lines, got)
		}
	}
}
