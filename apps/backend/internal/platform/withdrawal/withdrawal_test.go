package withdrawal

import "testing"

func TestNoticeApplies(t *testing.T) {
	t.Parallel()
	for _, c := range []string{"ES", "cz", " de ", "FR", "NO", "IS", "LI", "SE", "HR"} {
		if !NoticeApplies(c) {
			t.Errorf("NoticeApplies(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"IL", "GB", "US", "CH", "UA", "", "XX", "RU"} {
		if NoticeApplies(c) {
			t.Errorf("NoticeApplies(%q) = true, want false", c)
		}
	}
}

func TestListIsTwentySevenPlusThree(t *testing.T) {
	t.Parallel()
	if len(euEEA) != 30 {
		t.Fatalf("EU/EEA list has %d entries, want 27 EU + 3 EEA", len(euEEA))
	}
}

func TestSellerCountryPrefersLegalAddress(t *testing.T) {
	t.Parallel()
	il := "il"
	if got := SellerCountry(&il, "DE"); got != "IL" {
		t.Fatalf("SellerCountry = %q, want IL", got)
	}
	blank := "  "
	if got := SellerCountry(&blank, "es"); got != "ES" {
		t.Fatalf("SellerCountry blank legal = %q, want ES", got)
	}
	if got := SellerCountry(nil, "cz"); got != "CZ" {
		t.Fatalf("SellerCountry nil = %q, want CZ", got)
	}
	if ForSeller(&il, "DE") {
		t.Fatal("an Israeli legal address with a German org country must not show the notice")
	}
	if !ForSeller(nil, "DE") {
		t.Fatal("falls back to org country DE")
	}
	if ForSeller(nil, "") {
		t.Fatal("unknown country must not show the notice")
	}
}
