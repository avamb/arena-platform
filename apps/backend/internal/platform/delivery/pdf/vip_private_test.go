package pdf

import (
	"reflect"
	"strings"
	"testing"
)

// V1: the organizer's VIP mark of a ticket is private. The PDF e-ticket has
// no field to print it, so a mark can never change a byte of the document.
func TestTicket_HasNoVIPField(t *testing.T) {
	typ := reflect.TypeOf(Ticket{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "vip") {
			t.Errorf("pdf.Ticket field %q would print the organizer's private VIP mark", typ.Field(i).Name)
		}
	}
}
