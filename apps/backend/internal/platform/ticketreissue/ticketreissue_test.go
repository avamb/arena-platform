package ticketreissue

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"anna@example.com":   "a***@e******.com",
		"b@example.co.uk":    "b@e******.co.uk",
		"ivan@localhost":     "i***@l********",
		"not-an-address":     "***",
		"@example.com":       "***",
		"мария@почта.рф":     "м****@п****.рф",
		"x.y+tag@sub.dom.io": "x******@s**.dom.io",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFingerprint_IgnoresCaseAndSpacesOfTheAddressOnly(t *testing.T) {
	order := uuid.New()
	a := Fingerprint(order, " Buyer@Example.com ", ReasonWrongAddress, " note ")
	b := Fingerprint(order, "buyer@example.com", ReasonWrongAddress, "note")
	if a != b {
		t.Fatalf("the same request gave two fingerprints")
	}
	for name, other := range map[string]string{
		"order":  Fingerprint(uuid.New(), "buyer@example.com", ReasonWrongAddress, "note"),
		"email":  Fingerprint(order, "other@example.com", ReasonWrongAddress, "note"),
		"reason": Fingerprint(order, "buyer@example.com", ReasonOther, "note"),
		"note":   Fingerprint(order, "buyer@example.com", ReasonWrongAddress, "Note"),
	} {
		if other == a {
			t.Errorf("a different %s gave the same fingerprint", name)
		}
	}
}

func TestValidReason(t *testing.T) {
	for _, r := range []string{ReasonWrongAddress, ReasonForwarded, ReasonOther} {
		if !ValidReason(r) {
			t.Errorf("%q refused", r)
		}
	}
	for _, r := range []string{"", "typo", "SENT_TO_WRONG_ADDRESS"} {
		if ValidReason(r) {
			t.Errorf("%q accepted", r)
		}
	}
}

func TestTicketRefusal_UnwrapsToNotReissuable(t *testing.T) {
	var err error = &TicketRefusal{Number: 7, Reason: "review_hold"}
	if !errors.Is(err, ErrTicketNotReissable) {
		t.Fatal("a ticket refusal must answer ErrTicketNotReissable")
	}
}

func TestLetterFacts_TypoOnlyForWrongAddress(t *testing.T) {
	if !(LetterFacts{Reason: ReasonWrongAddress}).Typo() {
		t.Error("a wrong address is a typo letter")
	}
	for _, r := range []string{ReasonForwarded, ReasonOther} {
		if (LetterFacts{Reason: r}).Typo() {
			t.Errorf("%q is not a typo letter", r)
		}
	}
}

func TestProviderOf_StripeCanMoveTheReceiptAddress(t *testing.T) {
	if p := ProviderOf("stripe"); !p.EmailUpdatable || p.Title == "" {
		t.Errorf("stripe: %+v", p)
	}
	for _, name := range []string{"flitt", "allpay", "payplus"} {
		if p := ProviderOf(name); p.EmailUpdatable {
			t.Errorf("%s claims it can move the receipt address", name)
		}
	}
	if p := ProviderOf("unknown-provider"); p.EmailUpdatable {
		t.Error("an unknown provider claims it can move the receipt address")
	}
}

func TestMintToken_IsLongAndFresh(t *testing.T) {
	a, err := mintToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := mintToken()
	if len(a) != 64 || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
}
