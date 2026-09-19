package opsalert

import "testing"

func TestScrubEmails(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single email",
			in:   "smtp: delivery to buyer@example.com failed: mailbox full",
			want: "smtp: delivery to [redacted] failed: mailbox full",
		},
		{
			name: "no email",
			in:   "connection reset by peer",
			want: "connection reset by peer",
		},
		{
			name: "multiple emails",
			in:   "cc a@b.com bcc c@d.org failed",
			want: "cc [redacted] bcc [redacted] failed",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScrubEmails(tc.in); got != tc.want {
				t.Errorf("ScrubEmails(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("Truncate short string = %q, want unchanged", got)
	}
	got := Truncate("hello world", 5)
	if len([]rune(got)) != 5 {
		t.Errorf("Truncate length = %d, want 5 (got %q)", len([]rune(got)), got)
	}
	if got[len(got)-1] == 'o' {
		t.Errorf("Truncate should end with ellipsis marker, got %q", got)
	}
}

func TestFormatMinorUnits(t *testing.T) {
	cases := []struct {
		amount int64
		cur    string
		want   string
	}{
		{189000, "CZK", "1890.00 CZK"},
		{1890, "USD", "18.90 USD"},
		{5, "USD", "0.05 USD"},
		{0, "USD", "0.00 USD"},
		{-150, "USD", "-1.50 USD"},
	}
	for _, tc := range cases {
		if got := FormatMinorUnits(tc.amount, tc.cur); got != tc.want {
			t.Errorf("FormatMinorUnits(%d, %q) = %q, want %q", tc.amount, tc.cur, got, tc.want)
		}
	}
}
