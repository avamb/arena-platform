package main

import (
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/catalog/catalogimport"
)

func descEvent(desc string) catalogimport.Bil24SnapshotEvent {
	return catalogimport.Bil24SnapshotEvent{
		ExternalBil24ID: "df-1", Title: "T", StartsAt: time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC), Description: desc,
	}
}

// Bil24 sends HTML, so art is the default; --description-format text reads the
// old marks.
func TestDescriptionFormatFlag_DefaultArtAndTextOption(t *testing.T) {
	events := []catalogimport.Bil24SnapshotEvent{descEvent("<p>A <b>b</b></p>")}
	applyDescriptionFormat(events, catalogimport.DescriptionFormatART)
	got, err := events[0].DescriptionART()
	if err != nil || got != "<p>A <strong>b</strong></p>" {
		t.Fatalf("art: %q %v", got, err)
	}
	applyDescriptionFormat(events, catalogimport.DescriptionFormatText)
	got, err = events[0].DescriptionART()
	if err != nil || got != "<p>&lt;p&gt;A &lt;b&gt;b&lt;/b&gt;&lt;/p&gt;</p>" {
		t.Fatalf("text: %q %v", got, err)
	}
}

func TestDescriptionFormatFlag_UnknownValueIsRefusedBeforeAnyWork(t *testing.T) {
	err := run([]string{"--input", "nope.csv", "--org-id", "00000000-0000-0000-0000-000000000001", "--description-format", "html"})
	if err == nil || !strings.Contains(err.Error(), "description-format") {
		t.Fatalf("err = %v", err)
	}
}

func TestDescriptionFormatFlag_IsDefinedWithArtDefault(t *testing.T) {
	content, err := readFileContent("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"description-format", catalogimport.DescriptionFormatART`) {
		t.Error("--description-format must default to art")
	}
}

// A WordPress-style description keeps its paragraphs through the dry-run
// validation path too.
func TestDescriptionFormatFlag_WordPressParagraphs(t *testing.T) {
	events := []catalogimport.Bil24SnapshotEvent{descEvent("One\n\nTwo\nthree")}
	applyDescriptionFormat(events, catalogimport.DescriptionFormatART)
	valid, errs := validateRows(events)
	if len(errs) != 0 || len(valid) != 1 {
		t.Fatalf("validate: %v", errs)
	}
	got, _ := valid[0].DescriptionART()
	if got != "<p>One</p><p>Two<br>three</p>" {
		t.Fatalf("got %q", got)
	}
}
