package delivery

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

type fakeAccentQuerier struct {
	v   string
	err error
}

func (f fakeAccentQuerier) GetOrganizationAccentForTicket(context.Context, uuid.UUID) (string, error) {
	return f.v, f.err
}

func TestHexAccent(t *testing.T) {
	for in, want := range map[string]string{
		"#822121":               "#822121",
		" #ABCDEF ":             "#abcdef",
		"rgba(255,255,255,.72)": "",
		"#fff":                  "",
		"":                      "",
	} {
		if got := hexAccent(in); got != want {
			t.Errorf("hexAccent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveAccent(t *testing.T) {
	id := uuid.New()
	p := &Payload{}
	resolveAccent(context.Background(), fakeAccentQuerier{v: "#822121"}, id, p, slog.Default())
	if p.OrgAccentColor != "#822121" {
		t.Fatalf("got %q", p.OrgAccentColor)
	}

	p = &Payload{OrgAccentColor: "#000000"}
	resolveAccent(context.Background(), fakeAccentQuerier{v: "#822121"}, id, p, slog.Default())
	if p.OrgAccentColor != "#000000" {
		t.Fatalf("a hint must win, got %q", p.OrgAccentColor)
	}

	p = &Payload{}
	resolveAccent(context.Background(), fakeAccentQuerier{err: errors.New("down")}, id, p, slog.Default())
	if p.OrgAccentColor != "" {
		t.Fatalf("a failed read keeps the default, got %q", p.OrgAccentColor)
	}

	p = &Payload{}
	resolveAccent(context.Background(), struct{}{}, id, p, slog.Default())
	if p.OrgAccentColor != "" {
		t.Fatal("a querier without the read keeps the default")
	}
}

func TestBuildPDFTicket_UsesOrganizationAccent(t *testing.T) {
	p := Payload{OrgAccentColor: "#822121", OrgName: "Vino"}
	b := resolveBranding(context.Background(), nil, p, nil)
	if b.Branding.Accent != "#822121" {
		t.Fatalf("branding accent %q", b.Branding.Accent)
	}
	tk := buildPDFTicket(uuid.New(), p, b.Branding, nil, nil)
	if tk.AccentColor != "#822121" {
		t.Fatalf("pdf accent %q", tk.AccentColor)
	}
	p.OrgAccentColor = "rgba(1,2,3,.5)"
	b = resolveBranding(context.Background(), nil, p, nil)
	if b.Branding.Accent != "" {
		t.Fatalf("a non-hex primary must keep the platform accent, got %q", b.Branding.Accent)
	}
}
