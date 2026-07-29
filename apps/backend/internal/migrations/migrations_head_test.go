package migrations_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

func TestHead_ReturnsSQLFile(t *testing.T) {
	head, err := migrations.Head()
	if err != nil {
		t.Fatalf("Head() returned unexpected error: %v", err)
	}
	if head == "" {
		t.Fatal("Head() returned empty string")
	}
	if !strings.HasSuffix(head, ".sql") {
		t.Errorf("Head() = %q; want filename ending in .sql", head)
	}
}

func TestHead_NumericPrefixPositive(t *testing.T) {
	head, err := migrations.Head()
	if err != nil {
		t.Fatalf("Head() returned unexpected error: %v", err)
	}
	parts := strings.SplitN(head, "_", 2)
	if len(parts) < 1 {
		t.Fatalf("Head() = %q; expected underscore-separated numeric prefix", head)
	}
	v, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("Head() = %q; leading prefix %q is not numeric: %v", head, parts[0], err)
	}
	if v <= 0 {
		t.Errorf("Head() = %q; numeric prefix %d is not > 0", head, v)
	}
}

func TestHead_KnownCurrentHead(t *testing.T) {
	const expectedHead = "0074_catalog_role_grants.sql" // updated: 0074 audits organizer/agent catalog permissions (AB-1 / #406)
	head, err := migrations.Head()
	if err != nil {
		t.Fatalf("Head() returned unexpected error: %v", err)
	}
	if head != expectedHead {
		t.Errorf("Head() = %q; want %q\n"+
			"If a new migration was added, update this test to reflect the new head.",
			head, expectedHead)
	}
}
