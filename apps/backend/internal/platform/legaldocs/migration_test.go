package legaldocs

import (
	"regexp"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

const migrationFile = "0159_legal_documents.sql"

func migrationUp(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile(migrations.Dir + "/" + migrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", migrationFile, err)
	}
	s := string(raw)
	if i := strings.Index(s, "-- +goose Down"); i >= 0 {
		s = s[:i]
	}
	return s
}

// checkValues returns the quoted values of the IN (...) list of a CHECK
// constraint named name.
func checkValues(t *testing.T, sql, name string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?s)CONSTRAINT ` + regexp.QuoteMeta(name) + `\s+CHECK \([^)]*?IN \(([^)]*)\)`)
	m := re.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("constraint %s not found", name)
	}
	var out []string
	for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMigration0159_ChecksMatchGoConstants keeps every CHECK of the
// migration equal to its Go mirror (AGENTS.md: enum pairs drift silently).
func TestMigration0159_ChecksMatchGoConstants(t *testing.T) {
	up := migrationUp(t)
	pairs := []struct {
		constraint string
		goList     []string
	}{
		{"legal_documents_kind_check", Kinds},
		{"legal_documents_locale_check", Locales},
		{"legal_documents_status_check", Statuses},
		{"legal_renderings_refund_mode_check", RefundModes},
		{"organizations_refund_policy_mode_check", RefundModes},
		{"events_refund_policy_mode_check", RefundModes},
	}
	for _, p := range pairs {
		if got := checkValues(t, up, p.constraint); !sameList(got, p.goList) {
			t.Errorf("%s = %v, Go has %v", p.constraint, got, p.goList)
		}
	}
}

// TestSeededTemplates_Parse: the version 1 the migration publishes in every
// language parses with today's allowlist, is marked as a draft for the
// lawyer, and renders with no placeholder left over.
func TestSeededTemplates_Parse(t *testing.T) {
	up := migrationUp(t)
	m := regexp.MustCompile(`(?s)\$seed\$(.*?)\$seed\$`).FindStringSubmatch(up)
	if m == nil {
		t.Fatal("seed template not found")
	}
	tpl, err := Parse(m[1])
	if err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if !strings.HasPrefix(m[1], "DRAFT — awaiting legal review") {
		t.Error("the seeded terms must say they wait for the lawyer")
	}
	for _, l := range Locales {
		if !strings.Contains(up, "('"+l+"')") {
			t.Errorf("the seed does not publish %q", l)
		}
	}
	v := BuildValues(Data{Locale: "en", DocumentVersion: 1, Seller: Seller{LegalName: "X", Country: "ES"}, Promoter: &Promoter{Name: "P"}})
	out := Render(tpl, v, "en")
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") || !strings.Contains(out, "No right of withdrawal") {
		t.Fatalf("seed render: %s", out)
	}
}

func TestMigration0159_GrantsLegalManageToTheSuperadminOnly(t *testing.T) {
	up := migrationUp(t)
	if !strings.Contains(up, "('legal.manage',") || !strings.Contains(up, "WHERE  r.name = 'platform_superadmin'") {
		t.Fatal("legal.manage must be seeded and granted to platform_superadmin in the same migration")
	}
	if strings.Count(up, "INSERT INTO role_permissions") != 1 {
		t.Fatal("legal.manage goes to platform_superadmin only")
	}
}

func TestPlatformSellerName_MatchesTheLetters(t *testing.T) {
	if PlatformSellerName != templates.PlatformLegalName {
		t.Fatalf("PlatformSellerName %q != templates.PlatformLegalName %q", PlatformSellerName, templates.PlatformLegalName)
	}
}
