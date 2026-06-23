package permissions_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// migrationContent reads the 0008_rbac.sql migration file relative to this
// test file's location.
func migrationContent(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller not available")
		return ""
	}
	// Navigate: permissions/ → platform/ → internal/ → backend/ → migrations/sql/
	migFile := filepath.Join(
		filepath.Dir(thisFile), // permissions/
		"..", "..", "..",        // backend/
		"internal", "migrations", "sql", "0008_rbac.sql",
	)
	b, err := os.ReadFile(migFile)
	if err != nil {
		t.Fatalf("cannot read 0008_rbac.sql: %v", err)
	}
	return string(b)
}

// =============================================================================
// Step 1: roles, permissions, role_permissions migrations
// =============================================================================

func TestRBAC117_MigrationFileExists(t *testing.T) {
	content := migrationContent(t)
	if len(content) == 0 {
		t.Fatal("0008_rbac.sql is empty")
	}
}

func TestRBAC117_MigrationHasRolesTable(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "CREATE TABLE roles") {
		t.Error("migration must CREATE TABLE roles")
	}
}

func TestRBAC117_MigrationHasPermissionsTable(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "CREATE TABLE permissions") {
		t.Error("migration must CREATE TABLE permissions")
	}
}

func TestRBAC117_MigrationHasRolePermissionsTable(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "CREATE TABLE role_permissions") {
		t.Error("migration must CREATE TABLE role_permissions")
	}
}

func TestRBAC117_MigrationHasUserRolesTable(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "CREATE TABLE user_roles") {
		t.Error("migration must CREATE TABLE user_roles")
	}
}

func TestRBAC117_MigrationRolesHasOrgID(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "org_id") {
		t.Error("roles table must have org_id column for per-org RBAC scoping")
	}
}

func TestRBAC117_MigrationRolesPermissionsIsM2N(t *testing.T) {
	content := migrationContent(t)
	// The M:N join table must reference both roles and permissions as FKs.
	if !strings.Contains(content, "REFERENCES roles") {
		t.Error("role_permissions must REFERENCES roles")
	}
	if !strings.Contains(content, "REFERENCES permissions") {
		t.Error("role_permissions must REFERENCES permissions")
	}
}

func TestRBAC117_MigrationHasGooseDirectives(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration must have '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration must have '-- +goose Down' directive")
	}
}

// =============================================================================
// Step 4: Seed default permissions for built-in roles
// =============================================================================

func TestRBAC117_SeedsAdminRole(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "'admin'") {
		t.Error("migration must seed the 'admin' built-in role")
	}
}

func TestRBAC117_SeedsGeoAdminRole(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "'geo_admin'") {
		t.Error("migration must seed the 'geo_admin' built-in role")
	}
}

func TestRBAC117_SeedsScaffoldUserRole(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "'scaffold_user'") {
		t.Error("migration must seed the 'scaffold_user' built-in role")
	}
}

func TestRBAC117_SeedsGeoAdminPermission(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "'geo.admin'") {
		t.Error("migration must seed the 'geo.admin' permission")
	}
}

func TestRBAC117_SeedsScaffoldEchoCreatePermission(t *testing.T) {
	content := migrationContent(t)
	if !strings.Contains(content, "'scaffold.echo.create'") {
		t.Error("migration must seed the 'scaffold.echo.create' permission")
	}
}

func TestRBAC117_SeedsRolePermissionMappings(t *testing.T) {
	content := migrationContent(t)
	// Verify that role_permissions seed INSERTs are present.
	if !strings.Contains(content, "INSERT INTO role_permissions") {
		t.Error("migration must seed role_permissions mappings")
	}
}

func TestRBAC117_AdminRoleGetsAllPermissions(t *testing.T) {
	content := migrationContent(t)
	// The admin role seed selects from roles and permissions with no
	// permission filter so it gets everything.
	if !strings.Contains(content, "r.name = 'admin'") {
		t.Error("migration should assign all permissions to the 'admin' role")
	}
}
