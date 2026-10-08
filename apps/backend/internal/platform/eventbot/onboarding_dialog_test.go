package eventbot

import "testing"

func TestOnbFlagOf(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"ES": "🇪🇸", "cz": "🇨🇿", "": "", "E": "", "E1": "", "ESP": ""} {
		if got := flagOf(in); got != want {
			t.Errorf("flagOf(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestOnbDisplay(t *testing.T) {
	t.Parallel()
	f := OnbSchemaField{Key: "seating", Options: []OnbSchemaOption{{Value: "ga", Label: "General admission"}, {Value: "seated", Label: "Seated"}}}
	if got := onbDisplay(f, "ga"); got != "General admission" {
		t.Errorf("select label = %q", got)
	}
	if got := onbDisplay(f, []any{"seated", "ga", 7}); got != "Seated, General admission" {
		t.Errorf("multiselect labels = %q", got)
	}
	if got := onbDisplay(f, "free text"); got != "free text" {
		t.Errorf("unknown value should print as is, got %q", got)
	}
	if got := onbDisplay(f, 12.5); got != "12.5" {
		t.Errorf("number = %q", got)
	}
	if got := onbDisplay(f, true); got != "" {
		t.Errorf("a bool prints nothing, got %q", got)
	}
}

func TestOnbDialogsExpireAndClear(t *testing.T) {
	t.Parallel()
	d := newOnbDialogs()
	first := d.get(1)
	first.step = onbEmail
	if d.get(1).step != onbEmail {
		t.Fatal("a live dialog must be returned as it was")
	}
	d.clear(1)
	if d.get(1).step != onbIdle {
		t.Fatal("a cleared dialog starts empty")
	}
}
