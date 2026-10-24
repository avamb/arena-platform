package hbil24

// catalog_description_rt03_test.go - spec 44, RT-03b: GET_ALL_ACTIONS sends the
// description as richtext.Render of the stored text with the event's format
// flag. The sites print it as HTML.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func actionDescription(t *testing.T, stored *string, format string) (string, bool) {
	t.Helper()
	h := &Handler{}
	row := gen.EventRow{ID: uuid.New(), Name: "Desc", Status: "published", Description: stored}
	entry := h.buildActionEntry(context.Background(), row, 0, "", catalogAction{descriptionFormat: format})
	d, ok := entry["description"].(string)
	return d, ok
}

func ptr(s string) *string { return &s }

func TestBil24_RT03_ArtDescriptionKeepsMarkupAndLinksGetRelAndTarget(t *testing.T) {
	art := `<p>Join <strong>us</strong> at <a href="https://example.com/t">the site</a></p><ul><li>one</li></ul>`
	got, ok := actionDescription(t, ptr(art), "art")
	if !ok {
		t.Fatal("description is missing")
	}
	want := `<p>Join <strong>us</strong> at <a href="https://example.com/t" rel="noopener noreferrer" target="_blank">the site</a></p><ul><li>one</li></ul>`
	if got != want {
		t.Errorf("description =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBil24_RT03_TextDescriptionReachesTheSiteAsStrongNotAsterisks(t *testing.T) {
	got, ok := actionDescription(t, ptr("A **bold** word\n\n- one\n- two"), "text")
	if !ok {
		t.Fatal("description is missing")
	}
	if want := "<p>A <strong>bold</strong> word</p><ul><li>one</li><li>two</li></ul>"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
	if strings.Contains(got, "**") {
		t.Errorf("the site would show asterisks: %q", got)
	}
	// A format that was not read is the old text.
	got, _ = actionDescription(t, ptr("a < b and **x**"), "")
	if want := "<p>a &lt; b and <strong>x</strong></p>"; got != want {
		t.Errorf("unflagged description = %q, want %q", got, want)
	}
}

func TestBil24_RT03_HostileArtIsCleanedAndNoDescriptionIsOmitted(t *testing.T) {
	got, _ := actionDescription(t, ptr(`<p onclick="x()">a<script>alert(1)</script><a href="javascript:alert(2)">b</a></p>`), "art")
	for _, bad := range []string{"<script", "onclick", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Errorf("description %q contains %q", got, bad)
		}
	}
	if _, ok := actionDescription(t, nil, "art"); ok {
		t.Error("an event without a description must not carry the key")
	}
}
