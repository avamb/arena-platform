package salesnotify

import (
	"os"
	"strings"
	"testing"
)

var referralLocales = []string{"en", "ru", "cs", "he", "es"}

func TestReferralTexts_EveryLocaleHasEveryKey(t *testing.T) {
	data := map[string]any{"Org": "O", "Code": "C", "Number": 7, "Amount": "A"}
	for _, loc := range referralLocales {
		for _, k := range referralKeys {
			got := say(loc, k, data)
			if got == k || strings.TrimSpace(got) == "" || strings.Contains(got, "{{") || strings.Contains(got, "<no value>") {
				t.Errorf("%s / %s renders %q", loc, k, got)
			}
		}
	}
	// A missing key would fall back to English silently: look in the files.
	for _, loc := range referralLocales {
		raw, err := os.ReadFile("../i18n/locales/" + loc + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range referralKeys {
			if !strings.Contains(string(raw), "[\""+k+"\"]") {
				t.Errorf("%s.toml lacks %s", loc, k)
			}
		}
	}
	// A translation must not drop a placeholder of the English text.
	for _, k := range referralKeys {
		en := say("en", k, map[string]any{"Org": "ORG1", "Code": "CODE1", "Number": 4242, "Amount": "AMT1"})
		for _, loc := range referralLocales[1:] {
			got := say(loc, k, map[string]any{"Org": "ORG1", "Code": "CODE1", "Number": 4242, "Amount": "AMT1"})
			for _, v := range []string{"ORG1", "CODE1", "4242", "AMT1"} {
				if strings.Contains(en, v) && !strings.Contains(got, v) {
					t.Errorf("%s / %s dropped %s (en: %s)", loc, k, v, en)
				}
			}
		}
	}
}

func TestFormatReferralReversal_FactsPauseLineAndNoContacts(t *testing.T) {
	for _, loc := range referralLocales {
		for _, disputed := range []bool{false, true} {
			in := ReferralReversalInfo{Locale: loc, OrgName: "Org <b>", Code: "ANA-7K2P", FriendOrderNumber: 1043,
				AmountMinor: 500, Currency: "EUR", Disputed: disputed}
			plain := FormatReferralReversal(in)
			in.CodePaused = true
			paused := FormatReferralReversal(in)
			for _, want := range []string{"ANA-7K2P", "1043"} {
				if !strings.Contains(plain, want) {
					t.Errorf("%s disputed=%v: %q missing:\n%s", loc, disputed, want, plain)
				}
			}
			if !strings.Contains(plain, "5.00") && !strings.Contains(plain, "5,00") {
				t.Errorf("%s: the amount is missing:\n%s", loc, plain)
			}
			if strings.Contains(plain, "Org <b>") || !strings.Contains(plain, "Org &lt;b&gt;") {
				t.Errorf("%s: the organization name is not escaped:\n%s", loc, plain)
			}
			if strings.Contains(plain, "@") {
				t.Errorf("%s: the notice carries an address:\n%s", loc, plain)
			}
			if len(paused) <= len(plain) || !strings.HasPrefix(paused, plain) {
				t.Errorf("%s: the paused notice is not the plain one plus a line", loc)
			}
			if got := say(loc, "salesnotify.referral.code_paused", map[string]any{"Code": "ANA-7K2P"}); !strings.HasSuffix(paused, got) {
				t.Errorf("%s: the paused line is not last", loc)
			}
			if len([]rune(paused)) > 4096 {
				t.Errorf("%s: over Telegram's limit", loc)
			}
		}
		refunded := FormatReferralReversal(ReferralReversalInfo{Locale: loc, Code: "C", AmountMinor: 1, Currency: "EUR"})
		disputed := FormatReferralReversal(ReferralReversalInfo{Locale: loc, Code: "C", AmountMinor: 1, Currency: "EUR", Disputed: true})
		if refunded == disputed {
			t.Errorf("%s: a refund and a dispute read the same", loc)
		}
	}
}

// A pause is final (no route switches a code on again), so no language may
// promise that the organizer can switch it back on.
func TestReferralCodePausedPromisesNoReturn(t *testing.T) {
	for _, tc := range []struct{ loc, forbidden string }{
		{"en", "again"}, {"ru", "снова"}, {"es", "volver a activar"}, {"cs", "Znovu"}, {"he", "שוב"},
	} {
		got := say(tc.loc, "salesnotify.referral.code_paused", map[string]any{"Code": "ANA-7K2P"})
		if strings.Contains(got, tc.forbidden) {
			t.Errorf("%s: %q promises the code can be switched on again: %q", tc.loc, tc.forbidden, got)
		}
	}
}

func TestReferralPayoutTexts_EveryLocaleEveryKind(t *testing.T) {
	for _, loc := range referralLocales {
		raw, err := os.ReadFile("../i18n/locales/" + loc + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range referralPayoutKeys {
			if !strings.Contains(string(raw), "[\""+k+"\"]") {
				t.Errorf("%s.toml lacks %s", loc, k)
			}
		}
		for _, in := range []ReferralPayoutInfo{
			{Kind: "balance", Count: 3, AmountMinor: 4500},
			{Kind: "pending", Count: 2, AmountMinor: 4500},
			{Kind: "unpaid", Count: 1, AmountMinor: 4500, Code: "ANA-7K2P", FriendOrderNumber: 1043, BalanceTimeout: true},
			{Kind: "unpaid", Count: 1, AmountMinor: 4500, Code: "ANA-7K2P", FriendOrderNumber: 1043},
		} {
			in.Locale, in.OrgName, in.Currency = loc, "Org <b>", "EUR"
			got := FormatReferralPayout(in)
			if strings.Contains(got, "{{") || strings.Contains(got, "<no value>") || strings.Contains(got, "salesnotify.") {
				t.Errorf("%s %s: %q", loc, in.Kind, got)
			}
			if !strings.Contains(got, "45.00") && !strings.Contains(got, "45,00") {
				t.Errorf("%s %s: the amount is missing:\n%s", loc, in.Kind, got)
			}
			if !strings.Contains(got, "Org &lt;b&gt;") || strings.Contains(got, "@") {
				t.Errorf("%s %s: name not escaped or an address leaked:\n%s", loc, in.Kind, got)
			}
			if in.Kind == "unpaid" && (!strings.Contains(got, "ANA-7K2P") || !strings.Contains(got, "1043")) {
				t.Errorf("%s unpaid: the code or the order is missing:\n%s", loc, got)
			}
			if in.Kind != "unpaid" && !strings.Contains(got, "3") && !strings.Contains(got, "2") {
				t.Errorf("%s %s: the count is missing:\n%s", loc, in.Kind, got)
			}
		}
	}
}
