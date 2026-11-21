package horders

import "testing"

func TestCardLetter(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name                 string
		job, jobTo, lt, ltTo *string
		want                 string
		wantTo               *string
	}{
		{"no job, no letter", nil, nil, nil, nil, "none", nil},
		{"site letter only (no job)", nil, nil, s("opened"), s("a@x.test"), "opened", s("a@x.test")},
		{"job sent, provider says delivered", s("sent"), s("a@x.test"), s("delivered"), s("a@x.test"), "delivered", s("a@x.test")},
		{"job sent, letter bounced", s("sent"), s("a@x.test"), s("failed"), s("a@x.test"), "failed", s("a@x.test")},
		{"resend waiting beats an old letter", s("pending"), s("b@x.test"), s("opened"), s("a@x.test"), "pending", s("b@x.test")},
		{"job only", s("skipped"), nil, nil, nil, "skipped", nil},
		{"empty letter status falls back to the job", s("sent"), s("a@x.test"), s(""), nil, "sent", s("a@x.test")},
	}
	for _, c := range cases {
		got, to := cardLetter(c.job, c.jobTo, c.lt, c.ltTo)
		if got != c.want {
			t.Errorf("%s: status %q, want %q", c.name, got, c.want)
		}
		if (to == nil) != (c.wantTo == nil) || (to != nil && *to != *c.wantTo) {
			t.Errorf("%s: recipient %v, want %v", c.name, to, c.wantTo)
		}
	}
}
