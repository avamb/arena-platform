package salesnotify

import (
	"strings"
	"testing"
)

func TestFormatSale_TestOrderIsMarked(t *testing.T) {
	live := FormatSale(Sale{OrgName: "Org", EventName: "Ev", OrderNumber: 1})
	if strings.Contains(live, "TEST") {
		t.Fatalf("a live sale must not say TEST:\n%s", live)
	}
	test := FormatSale(Sale{OrgName: "Org", EventName: "Ev", OrderNumber: 1, IsTest: true})
	if !strings.Contains(test, "<b>TEST</b> · <b>New sale</b>") {
		t.Fatalf("a test sale must be prefixed with TEST:\n%s", test)
	}
}

func TestFormatRefund_TestOrderIsMarked(t *testing.T) {
	if strings.Contains(FormatRefund(Refund{OrgName: "Org", Amount: 100}), "TEST") {
		t.Fatal("a live refund must not say TEST")
	}
	if !strings.Contains(FormatRefund(Refund{OrgName: "Org", Amount: 100, IsTest: true}), "<b>TEST</b> · <b>Refund</b>") {
		t.Fatal("a test refund must be prefixed with TEST")
	}
}
