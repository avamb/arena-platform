package hbot

import (
	"testing"

	"github.com/google/uuid"
)

// Only the shop paths of the web event center are a target (spec 61 section
// 4.5); everything else is dropped before any membership is read.
func TestWebTargetPattern_ShapesOfTheShop(t *testing.T) {
	t.Parallel()
	org := uuid.NewString()
	for target, want := range map[string]bool{
		"/o/" + org + "/shop":                       true,
		"/o/" + org + "/shop/settings":              true,
		"/o/" + org + "/shop/orders/" + org:         true,
		"/o/" + org + "/shop/products/abc_def-1":    true,
		"/o/" + org + "/events":                     false,
		"/o/" + org + "/shop/":                      false,
		"/o/" + org + "/shop/../x":                  false,
		"/o/" + org + "/shop?next=1":                false,
		"/o/" + org + "/shop#frag":                  false,
		"/o/" + org + "/shop/a b":                   false,
		"/o/" + org + "/shopping":                   false,
		"/o/not-an-id/shop":                         false,
		"//evil.test/o/" + org + "/shop":            false,
		"https://evil.test/o/" + org + "/shop":      false,
		"o/" + org + "/shop":                        false,
		"":                                          false,
		"/o/" + org + "/shop/%2e%2e":                false,
		"/o/" + org + "/shop/orders/\n/x":           false,
		"/o/" + org + "/shop/orders/" + org + "/..": false,
	} {
		if got := webTargetPattern.MatchString(target); got != want {
			t.Errorf("%q: matched=%v, want %v", target, got, want)
		}
	}
}
