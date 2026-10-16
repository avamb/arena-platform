package gen

import (
	"reflect"
	"strings"
	"testing"
)

// V1: the organizer's VIP mark is private. The query every letter and PDF
// resolves its presentation from must never select it, so neither can print it.
func TestTicketPresentation_NeverSelectsTheVIPMark(t *testing.T) {
	if strings.Contains(strings.ToLower(getTicketPresentationByID), "vip") {
		t.Error("GetTicketPresentationByID selects the VIP mark")
	}
	typ := reflect.TypeOf(TicketPresentationRow{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "vip") {
			t.Errorf("TicketPresentationRow field %q carries the VIP mark", typ.Field(i).Name)
		}
	}
}
