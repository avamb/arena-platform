package eventbot

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

func TestParseOrgField(t *testing.T) {
	country, _ := orgFieldByCode("k")
	if v, key := parseOrgField(country, " es "); key != "" || v != "ES" {
		t.Errorf("country es = %q %q", v, key)
	}
	for _, bad := range []string{"Spain", "E", "1A", "ÑA"} {
		if _, key := parseOrgField(country, bad); key != "bot.og.err_country" {
			t.Errorf("country %q must be refused, got %q", bad, key)
		}
	}
	form, _ := orgFieldByCode("f")
	if v, key := parseOrgField(form, "  S.L.  "); key != "" || v != "S.L." {
		t.Errorf("form = %q %q", v, key)
	}
	if _, key := parseOrgField(form, strings.Repeat("x", 81)); key != "bot.og.err_legal_form" {
		t.Errorf("an 81-rune form must be refused, got %q", key)
	}
	name, _ := orgFieldByCode("n")
	if v, _ := parseOrgField(name, "Teatro\n Ejemplo   S.L."); v != "Teatro Ejemplo S.L." {
		t.Errorf("a name is folded onto one line, got %q", v)
	}
	if _, key := parseOrgField(name, "   "); key != "bot.og.err_legal_name" {
		t.Errorf("an empty name must be refused, got %q", key)
	}
}

func TestParseOrgTax(t *testing.T) {
	if v, key := parseOrgTax(sellerinfo.SchemeEUVAT, "es b-1234 5678"); key != "" || v != "ESB12345678" {
		t.Errorf("eu vat = %q %q", v, key)
	}
	if _, key := parseOrgTax(sellerinfo.SchemeEUVAT, "B12345678"); key != "bot.og.err_tax_eu_vat" {
		t.Errorf("a bare NIF under EU VAT must be refused with its own hint, got %q", key)
	}
	if v, key := parseOrgTax(sellerinfo.SchemeOther, " B12345678 "); key != "" || v != "B12345678" {
		t.Errorf("other = %q %q", v, key)
	}
}

// Every field, scheme and missing-field code the screens can name has its
// text keys, so no screen ever prints a raw key.
func TestOrgDataKeysCoverEveryCode(t *testing.T) {
	have := map[string]bool{}
	for _, k := range orgDataKeys {
		have[k] = true
	}
	need := []string{}
	for _, f := range orgFields {
		need = append(need, "bot.og.btn_"+f.Key, "bot.og.q_"+f.Key, "bot.og.err_"+f.Key)
	}
	for _, s := range sellerinfo.Schemes {
		need = append(need, "bot.og.scheme_"+s, "bot.og.q_tax_"+s, "bot.og.err_tax_"+s)
	}
	for _, m := range sellerinfo.Missing(sellerinfo.Profile{}) {
		need = append(need, "bot.og.m_"+m)
	}
	for _, k := range need {
		if !have[k] {
			t.Errorf("missing text key %s", k)
		}
	}
}
