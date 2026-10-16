package delivery

import (
	"reflect"
	"strings"
	"testing"
)

// V1: the VIP mark is not part of what a letter or the PDF print, so the
// delivery job's payload has no field for it.
func TestPayload_HasNoVIPField(t *testing.T) {
	typ := reflect.TypeOf(Payload{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "vip") {
			t.Errorf("delivery.Payload field %q would carry the private VIP mark into a letter", typ.Field(i).Name)
		}
	}
}
