package htickets

import "testing"

func TestValidResendEmail(t *testing.T) {
	good := []string{"a@b.co", "anna.nova+tag@example.com", "x_y@sub.example.org", "ünï@example.com"}
	bad := []string{
		"", "plain", "a@b", "a@.com", "a@b.", "@example.com", "a b@example.com",
		"Name <a@example.com>", "a@example.com, b@example.com", "a@example.com;b@example.com",
		"\"quoted\"@example.com", "a@@example.com",
	}
	for _, s := range good {
		if !ValidResendEmail(s) {
			t.Errorf("%q must be valid", s)
		}
	}
	for _, s := range bad {
		if ValidResendEmail(s) {
			t.Errorf("%q must be invalid", s)
		}
	}
	long := make([]byte, 260)
	for i := range long {
		long[i] = 'a'
	}
	if ValidResendEmail(string(long) + "@example.com") {
		t.Error("an address over 254 characters must be invalid")
	}
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"anna@example.com":       "a***@e******.com",
		"a@b.co":                 "a@b.co",
		"boris@mail.example.org": "b****@m***.example.org",
		"nonsense":               "***",
	} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
