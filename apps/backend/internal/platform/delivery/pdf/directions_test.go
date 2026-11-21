package pdf

import (
	"bytes"
	"context"
	"testing"
)

const (
	testGoogle = "https://www.google.com/maps/search/?api=1&query=32.0853,34.7818"
	testWaze   = "https://waze.com/ul?ll=32.0853,34.7818&navigate=yes"
)

// The directions line (owner decision 2026-11-07) is a muted line under the
// venue address with two clickable links; without URLs nothing is printed.
func TestRender_DirectionsLine(t *testing.T) {
	tk := validTicket(t)
	tk.Locale = "ru"
	tk.DirectionsGoogleURL, tk.DirectionsWazeURL = testGoogle, testWaze
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/URI (" + testGoogle + ")", "/URI (" + testWaze + ")"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing link annotation %q", want)
		}
	}
	for _, want := range []string{"Как добраться", "Google\u00a0Maps", "Waze"} {
		if !bytes.Contains(out, utf16beEscaped(want)) && !bytes.Contains(out, pdfTextPrefix(want)) {
			t.Errorf("content stream lacks %q", want)
		}
	}
	again, err := Render(context.Background(), tk)
	if err != nil || !bytes.Equal(out, again) {
		t.Error("render with directions is not byte-deterministic")
	}

	tk.DirectionsGoogleURL, tk.DirectionsWazeURL = "", ""
	out, err = Render(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("/URI (https://www.google.com")) || bytes.Contains(out, []byte("waze.com")) {
		t.Error("link annotation present without URLs")
	}
	for _, s := range []string{"Как добраться", "Google\u00a0Maps", "Waze"} {
		if bytes.Contains(out, utf16beEscaped(s)) || bytes.Contains(out, pdfTextPrefix(s)) {
			t.Errorf("%q printed without URLs", s)
		}
	}
}

// Every locale of the string table names the directions line, and the line
// never moves the anchored code block.
func TestRender_DirectionsEveryLocaleKeepsCodeBlock(t *testing.T) {
	for loc, s := range ticketStringsByLocale {
		if s.Directions == "" {
			t.Errorf("%s: empty Directions label", loc)
		}
		tk := validTicket(t)
		tk.Locale = loc
		plain, err := Render(context.Background(), tk)
		if err != nil {
			t.Fatal(err)
		}
		tk.DirectionsGoogleURL, tk.DirectionsWazeURL = testGoogle, testWaze
		with, err := Render(context.Background(), tk)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(with, []byte("/URI ("+testWaze+")")) {
			t.Errorf("%s: no Waze annotation", loc)
		}
		if lowestInk(t, with) != lowestInk(t, plain) {
			t.Errorf("%s: the directions line moved the bottom block", loc)
		}
	}
}
