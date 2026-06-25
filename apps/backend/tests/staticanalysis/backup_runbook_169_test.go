// Package staticanalysis — Feature #169: Backup / Restore Runbook
//
// These tests verify that all required operational artifacts exist and
// contain the key content mandated by the feature specification:
//
//	Step 1: Backup script (pg_dump + WAL archive)
//	Step 2: Restore runbook (markdown)
//	Step 3: Staging dry-run
//	Step 4: RPO/RTO assumptions documented
//
// The tests are static: they read the deploy/ directory relative to the repo
// root and assert presence + content.  No database or network is required.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// deployDir returns the absolute path to the deploy/ directory at repo root.
func deployDir(tb testing.TB) string {
	tb.Helper()
	return filepath.Join(repoRoot(tb), "deploy")
}

// readDeployFile reads a file from the deploy/ directory and returns its
// content as a string.  The test is fatally failed if the file does not exist.
func readDeployFile(tb testing.TB, name string) string {
	tb.Helper()
	path := filepath.Join(deployDir(tb), name)
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("deploy/%s must exist: %v", name, err)
	}
	return string(data)
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Backup script (pg_dump + WAL archive)
// ─────────────────────────────────────────────────────────────────────────────

func TestBackup169_BackupScriptExists(t *testing.T) {
	path := filepath.Join(deployDir(t), "backup.sh")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deploy/backup.sh must exist: %v", err)
	}
}

func TestBackup169_BackupScriptHasPgDump(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	if !strings.Contains(content, "pg_dump") {
		t.Error("deploy/backup.sh must contain pg_dump invocation")
	}
}

func TestBackup169_BackupScriptHasCustomFormat(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	// Custom format enables parallel restore and partial restore
	if !strings.Contains(content, "--format=custom") && !strings.Contains(content, "-Fc") {
		t.Error("deploy/backup.sh must use pg_dump --format=custom (-Fc) for efficient restores")
	}
}

func TestBackup169_BackupScriptHasWALBaseBackup(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	if !strings.Contains(content, "pg_basebackup") {
		t.Error("deploy/backup.sh must include pg_basebackup invocation for WAL archive support")
	}
}

func TestBackup169_BackupScriptHasRetention(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	if !strings.Contains(content, "BACKUP_RETENTION") {
		t.Error("deploy/backup.sh must implement backup retention via BACKUP_RETENTION variable")
	}
}

func TestBackup169_BackupScriptHasDatabaseURLVar(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	if !strings.Contains(content, "DATABASE_URL") {
		t.Error("deploy/backup.sh must accept DATABASE_URL environment variable")
	}
}

func TestBackup169_BackupScriptHasChecksumGeneration(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	if !strings.Contains(content, "sha256") {
		t.Error("deploy/backup.sh must generate SHA-256 checksum for the dump file")
	}
}

func TestBackup169_BackupScriptHasRedisNote(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	// Redis must NOT be backed up — this must be documented in the backup script
	if !strings.Contains(content, "Redis") {
		t.Error("deploy/backup.sh must document Redis backup policy (no backup required)")
	}
}

func TestBackup169_BackupScriptHasDryRunFlag(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	if !strings.Contains(content, "dry-run") && !strings.Contains(content, "DRY_RUN") {
		t.Error("deploy/backup.sh must support --dry-run flag for safe testing")
	}
}

func TestBackup169_BackupScriptHasErrorHandling(t *testing.T) {
	content := readDeployFile(t, "backup.sh")
	// set -euo pipefail is the standard bash error-safety header
	if !strings.Contains(content, "set -e") && !strings.Contains(content, "pipefail") {
		t.Error("deploy/backup.sh must use 'set -euo pipefail' for error safety")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Restore script + runbook (markdown)
// ─────────────────────────────────────────────────────────────────────────────

func TestBackup169_RestoreScriptExists(t *testing.T) {
	path := filepath.Join(deployDir(t), "restore.sh")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deploy/restore.sh must exist: %v", err)
	}
}

func TestBackup169_RestoreScriptHasPgRestore(t *testing.T) {
	content := readDeployFile(t, "restore.sh")
	if !strings.Contains(content, "pg_restore") {
		t.Error("deploy/restore.sh must use pg_restore to restore from custom-format dumps")
	}
}

func TestBackup169_RestoreScriptHasDumpFileFlag(t *testing.T) {
	content := readDeployFile(t, "restore.sh")
	if !strings.Contains(content, "--dump-file") && !strings.Contains(content, "DUMP_FILE") {
		t.Error("deploy/restore.sh must accept a --dump-file argument")
	}
}

func TestBackup169_RestoreScriptHasChecksumVerification(t *testing.T) {
	content := readDeployFile(t, "restore.sh")
	if !strings.Contains(content, "sha256") {
		t.Error("deploy/restore.sh must verify SHA-256 checksum before restore")
	}
}

func TestBackup169_RestoreScriptHasRedisFlushNote(t *testing.T) {
	content := readDeployFile(t, "restore.sh")
	if !strings.Contains(content, "Redis") {
		t.Error("deploy/restore.sh must document Redis flush/restart requirement after restore")
	}
}

func TestBackup169_RestoreScriptHasMigrationsNote(t *testing.T) {
	content := readDeployFile(t, "restore.sh")
	if !strings.Contains(content, "arena-migrate") {
		t.Error("deploy/restore.sh must note that arena-migrate up must run after restore")
	}
}

func TestBackup169_RunbookExists(t *testing.T) {
	path := filepath.Join(deployDir(t), "BACKUP_RESTORE_RUNBOOK.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deploy/BACKUP_RESTORE_RUNBOOK.md must exist: %v", err)
	}
}

func TestBackup169_RunbookHasPostgresSection(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	if !strings.Contains(content, "PostgreSQL") && !strings.Contains(content, "pg_dump") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must cover PostgreSQL backup procedure")
	}
}

func TestBackup169_RunbookHasRedisSection(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	if !strings.Contains(content, "Redis") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must document Redis policy (no backup required)")
	}
}

func TestBackup169_RunbookHasRestoreSteps(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// Must contain a numbered restore procedure
	reHasRestoreSteps := regexp.MustCompile(`(?i)(restore|step\s+\d+)`)
	if !reHasRestoreSteps.MatchString(content) {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must contain step-by-step restore procedure")
	}
}

func TestBackup169_RunbookHasTableOfContents(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// Table of contents is a usability requirement for runbooks
	if !strings.Contains(content, "Table of Contents") && !strings.Contains(content, "## ") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must contain a Table of Contents or section headers")
	}
}

func TestBackup169_RunbookHasVerificationChecklist(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// Post-restore verification steps
	if !strings.Contains(content, "- [ ]") && !strings.Contains(content, "healthz") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must include a post-restore verification checklist")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Staging dry-run
// ─────────────────────────────────────────────────────────────────────────────

func TestBackup169_DryRunScriptExists(t *testing.T) {
	path := filepath.Join(deployDir(t), "staging-dryrun.sh")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deploy/staging-dryrun.sh must exist: %v", err)
	}
}

func TestBackup169_DryRunScriptInvokesBackup(t *testing.T) {
	content := readDeployFile(t, "staging-dryrun.sh")
	if !strings.Contains(content, "backup.sh") {
		t.Error("deploy/staging-dryrun.sh must invoke backup.sh to perform the backup step")
	}
}

func TestBackup169_DryRunScriptInvokesRestore(t *testing.T) {
	content := readDeployFile(t, "staging-dryrun.sh")
	if !strings.Contains(content, "restore.sh") {
		t.Error("deploy/staging-dryrun.sh must invoke restore.sh to perform the restore step")
	}
}

func TestBackup169_DryRunScriptVerifiesDataIntegrity(t *testing.T) {
	content := readDeployFile(t, "staging-dryrun.sh")
	// Must verify that data can be queried after restore
	if !strings.Contains(content, "SELECT") && !strings.Contains(content, "COUNT") {
		t.Error("deploy/staging-dryrun.sh must query restored data to verify integrity")
	}
}

func TestBackup169_DryRunScriptVerifiesHealthEndpoints(t *testing.T) {
	content := readDeployFile(t, "staging-dryrun.sh")
	if !strings.Contains(content, "healthz") && !strings.Contains(content, "readyz") {
		t.Error("deploy/staging-dryrun.sh must verify /healthz and /readyz after restore")
	}
}

func TestBackup169_DryRunScriptUsesArenaImage(t *testing.T) {
	content := readDeployFile(t, "staging-dryrun.sh")
	if !strings.Contains(content, "ARENA_IMAGE") {
		t.Error("deploy/staging-dryrun.sh must use ARENA_IMAGE variable for the Docker image")
	}
}

func TestBackup169_DryRunScriptRunsMigrations(t *testing.T) {
	content := readDeployFile(t, "staging-dryrun.sh")
	if !strings.Contains(content, "arena-migrate") {
		t.Error("deploy/staging-dryrun.sh must run arena-migrate after restore")
	}
}

func TestBackup169_DryRunScriptHasCleanup(t *testing.T) {
	content := readDeployFile(t, "staging-dryrun.sh")
	// Must clean up test containers
	if !strings.Contains(content, "cleanup") && !strings.Contains(content, "docker rm") {
		t.Error("deploy/staging-dryrun.sh must clean up Docker containers after dry-run")
	}
}

func TestBackup169_RunbookDocumentsDryRunProcedure(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	if !strings.Contains(content, "Dry-Run") && !strings.Contains(content, "dry-run") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must document the staging dry-run procedure")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: RPO/RTO assumptions documented
// ─────────────────────────────────────────────────────────────────────────────

func TestBackup169_RunbookHasRPO(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	if !strings.Contains(content, "RPO") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must document Recovery Point Objective (RPO)")
	}
}

func TestBackup169_RunbookHasRTO(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	if !strings.Contains(content, "RTO") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must document Recovery Time Objective (RTO)")
	}
}

func TestBackup169_RunbookHasRPORTOTable(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// RPO and RTO must appear in close proximity (within the same table or section)
	rpoIdx := strings.Index(content, "RPO")
	rtoIdx := strings.Index(content, "RTO")
	if rpoIdx == -1 || rtoIdx == -1 {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must contain both RPO and RTO")
		return
	}
	distance := rtoIdx - rpoIdx
	if distance < 0 {
		distance = rpoIdx - rtoIdx
	}
	if distance > 2000 {
		t.Errorf("RPO and RTO are too far apart (%d chars); they should appear in the same section or table", distance)
	}
}

func TestBackup169_RunbookQuantifiesRPO(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// RPO must be quantified — look for time units near "RPO"
	reTimeUnit := regexp.MustCompile(`(?i)(RPO|recovery point).{0,200}(hour|minute|second|24 h|24h|\d+ min)`)
	if !reTimeUnit.MatchString(content) {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must quantify RPO with a concrete time value (e.g., '~24 hours')")
	}
}

func TestBackup169_RunbookQuantifiesRTO(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// RTO must be quantified — look for time units near "RTO"
	reTimeUnit := regexp.MustCompile(`(?i)(RTO|recovery time).{0,200}(hour|minute|second|60 min|\d+ min)`)
	if !reTimeUnit.MatchString(content) {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must quantify RTO with a concrete time value (e.g., '15–60 min')")
	}
}

func TestBackup169_RunbookDistinguishesTiers(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// Should distinguish between pg_dump (Tier 1) and WAL/PITR (Tier 2)
	hasTier1 := strings.Contains(content, "Tier 1") || strings.Contains(content, "tier 1") ||
		strings.Contains(content, "pg_dump") && strings.Contains(content, "nightly")
	hasTier2 := strings.Contains(content, "Tier 2") || strings.Contains(content, "PITR") ||
		strings.Contains(content, "WAL")
	if !hasTier1 || !hasTier2 {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must document both backup tiers: pg_dump (Tier 1) and WAL/PITR (Tier 2)")
	}
}

func TestBackup169_RunbookHasMonitoringSection(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	if !strings.Contains(content, "Monitor") && !strings.Contains(content, "Alert") {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must include a monitoring/alerting section")
	}
}

func TestBackup169_RunbookDocumentsRedisNoBackup(t *testing.T) {
	content := readDeployFile(t, "BACKUP_RESTORE_RUNBOOK.md")
	// Must explicitly state Redis does NOT need backup
	reNoBackup := regexp.MustCompile(`(?i)redis.{0,200}(no backup|not.*backup|operational state|rebuilt from)`)
	if !reNoBackup.MatchString(content) {
		t.Error("BACKUP_RESTORE_RUNBOOK.md must explicitly state Redis does not require backup (operational state only)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Meta: all four feature steps are covered
// ─────────────────────────────────────────────────────────────────────────────

func TestBackup169_AllStepsCovered(t *testing.T) {
	t.Run("Step1_BackupScript", TestBackup169_BackupScriptExists)
	t.Run("Step1_PgDump", TestBackup169_BackupScriptHasPgDump)
	t.Run("Step1_WALArchive", TestBackup169_BackupScriptHasWALBaseBackup)
	t.Run("Step2_RestoreScript", TestBackup169_RestoreScriptExists)
	t.Run("Step2_Runbook", TestBackup169_RunbookExists)
	t.Run("Step3_DryRun", TestBackup169_DryRunScriptExists)
	t.Run("Step3_DryRunVerification", TestBackup169_DryRunScriptVerifiesDataIntegrity)
	t.Run("Step4_RPO", TestBackup169_RunbookHasRPO)
	t.Run("Step4_RTO", TestBackup169_RunbookHasRTO)
	t.Run("Step4_Quantified", TestBackup169_RunbookQuantifiesRPO)
}
