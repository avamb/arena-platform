package hcatalog

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeSeriesName(t *testing.T) {
	if got, ok := NormalizeSeriesName("  Summer \n tour  "); !ok || got != "Summer tour" {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := NormalizeSeriesName("   "); ok {
		t.Error("a blank name must be refused")
	}
	if _, ok := NormalizeSeriesName(strings.Repeat("я", 201)); ok {
		t.Error("201 characters must be refused")
	}
	if _, ok := NormalizeSeriesName(strings.Repeat("я", 200)); !ok {
		t.Error("200 characters must pass")
	}
}

func TestNormalizeSeriesVideoURL(t *testing.T) {
	good := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://vimeo.com/123456789",
	}
	for _, in := range good {
		if v, ok := NormalizeSeriesVideoURL(in); !ok || v == nil {
			t.Errorf("%q must pass", in)
		}
	}
	if v, ok := NormalizeSeriesVideoURL("  "); !ok || v != nil {
		t.Error("blank means not set")
	}
	bad := []string{
		"http://youtu.be/dQw4w9WgXcQ",
		"https://example.com/video.mp4",
		"https://rutube.ru/video/0123456789abcdef0123456789abcdef/",
		"not a url",
		"https://www.youtube.com/@channel",
	}
	for _, in := range bad {
		if _, ok := NormalizeSeriesVideoURL(in); ok {
			t.Errorf("%q must be refused", in)
		}
	}
}

func TestNormalizeSeriesHTML(t *testing.T) {
	if v, err := normalizeSeriesHTML("   "); err != nil || v != nil {
		t.Errorf("blank is not set: %v %v", v, err)
	}
	v, err := normalizeSeriesHTML("<p>Hello <strong>world</strong></p><script>alert(1)</script>")
	if err == nil && v != nil && strings.Contains(*v, "<script") {
		t.Errorf("script must not survive: %q", *v)
	}
	if v, err := normalizeSeriesHTML("<p>Plain</p>"); err != nil || v == nil || !strings.Contains(*v, "Plain") {
		t.Errorf("plain paragraph: %v %v", v, err)
	}
}

type fakeSeriesSlugChecker map[string]bool

func (f fakeSeriesSlugChecker) SeriesSlugTaken(_ context.Context, _ uuid.UUID, slug string, _ *uuid.UUID) (bool, error) {
	return f[slug], nil
}

func TestAutoSeriesSlug(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()
	got, err := autoSeriesSlug(ctx, fakeSeriesSlugChecker{}, org, "Летний тур 2027")
	if err != nil || got == "" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, ok := seriesSlugValid(got); !ok {
		t.Errorf("%q is not a valid slug", got)
	}
	got, err = autoSeriesSlug(ctx, fakeSeriesSlugChecker{"summer-tour": true, "summer-tour-2": true}, org, "Summer Tour")
	if err != nil || got != "summer-tour-3" {
		t.Errorf("suffix search: %q %v", got, err)
	}
	got, err = autoSeriesSlug(ctx, fakeSeriesSlugChecker{}, org, "!!!")
	if err != nil || got != "tour" {
		t.Errorf("nothing survives: %q %v", got, err)
	}
}
