package invoicing

import (
	"errors"
	"math/rand"
	"regexp"
	"slices"
	"testing"
)

func TestCardFee_GrossesUpSoThePlatformNetsTheInvoice(t *testing.T) {
	cases := []struct {
		total int64
		bp    int32
		fixed int64
		want  int64
	}{
		{1650, 150, 25, 51}, // ceil(1675 × 10000 / 9850) = 1701
		{1650, 0, 0, 0},
		{0, 150, 25, 0}, // nothing to pay, no fee
		{10000, 290, 30, 330},
	}
	for _, c := range cases {
		got, err := CardFee(c.total, c.bp, c.fixed)
		if err != nil || got != c.want {
			t.Errorf("CardFee(%d, %d, %d) = %d, %v; want %d", c.total, c.bp, c.fixed, got, err, c.want)
		}
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 50000; i++ {
		total, bp, fixed := rng.Int63n(10_000_000), int32(rng.Intn(9000)), rng.Int63n(500)
		fee, err := CardFee(total, bp, fixed)
		if err != nil {
			t.Fatal(err)
		}
		if total == 0 {
			continue
		}
		charged := total + fee
		// What the provider leaves: charged − bp of it − fixed, never below
		// the invoice, and the fee is never a whole unit more than needed.
		if net := charged*int64(10000-bp) - fixed*10000; net < total*10000 {
			t.Fatalf("total %d bp %d fixed %d: fee %d nets %d/10000", total, bp, fixed, fee, net)
		}
		if lower := (charged-1)*int64(10000-bp) - fixed*10000; lower >= total*10000 {
			t.Fatalf("total %d bp %d fixed %d: fee %d is one unit too high", total, bp, fixed, fee)
		}
	}
	for _, bad := range [][3]int64{{-1, 100, 0}, {100, 10000, 0}, {100, -1, 0}, {100, 100, -1}} {
		if _, err := CardFee(bad[0], int32(bad[1]), bad[2]); !errors.Is(err, ErrBadInput) {
			t.Errorf("CardFee%v: err %v, want ErrBadInput", bad, err)
		}
	}
}

func TestPaymentReference_FormatAndFinding(t *testing.T) {
	shape := regexp.MustCompile(`^ARN-[A-HJKMNP-TV-Z2-9]{4}-[A-HJKMNP-TV-Z2-9]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		ref, err := NewPaymentReference()
		if err != nil {
			t.Fatal(err)
		}
		if !shape.MatchString(ref) {
			t.Fatalf("reference %q has the wrong shape", ref)
		}
		seen[ref] = true
		// A reference whose body starts with ARN (ARN-ARNW-KZAK) also yields
		// the overlapping candidate ARN-WKZA-K...; every candidate is checked
		// against the invoices, so only the real one must come first.
		if got := FindReferences("Invoice ARENA-2026-000017 / " + ref + " thanks"); len(got) == 0 || got[0] != ref {
			t.Fatalf("FindReferences missed %q: %v", ref, got)
		}
	}
	if len(seen) < 1990 {
		t.Errorf("only %d distinct references of 2000", len(seen))
	}
	for text, want := range map[string]string{
		"arn 7kqm x2tb":            "ARN-7KQM-X2TB",
		"Payment ARN-7KQM-X2TB.":   "ARN-7KQM-X2TB",
		"ref:arn.7kqm.x2tb, EUR 5": "ARN-7KQM-X2TB",
	} {
		if got := FindReferences(text); len(got) != 1 || got[0] != want {
			t.Errorf("FindReferences(%q) = %v, want [%s]", text, got, want)
		}
	}
	// "ARN" typed twice: the real reference must still be among the candidates.
	if got := FindReferences("ref ARN ARN-7KQM-X2TB"); !slices.Contains(got, "ARN-7KQM-X2TB") {
		t.Errorf("FindReferences missed the reference after a doubled prefix: %v", got)
	}
	if got := FindReferences("no code here, ARN-0000-1111 has banned letters"); len(got) != 0 {
		t.Errorf("found %v in a text without a valid reference", got)
	}
}
