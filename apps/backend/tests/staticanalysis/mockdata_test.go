// Package staticanalysis enforces the "no mock data" invariants required
// by feature #4 in the AutoForge backlog.
//
// Every test in this file is a static analysis: it walks the repository tree
// (relative to the discovered go.mod root) and asserts that prohibited
// patterns indicating in-memory stores, JavaScript mock backends, or stub
// persistence layers are NOT present in production code. Run with:
//
//	go test ./apps/backend/tests/staticanalysis/...
//
// All assertions are negative — the test passes when grep would return
// exit code 1 (no matches). Each violation is reported with file path and
// line number so the offending code can be found and fixed.
//
// The repository ships with a real PostgreSQL adapter (pgx/v5 + sqlc). If
// any of these tests start failing, an in-memory shortcut has crept into
// the codebase and persistence is silently faked. Replace the shortcut with
// a real database query before re-running the suite.
//
// The package name matches the directory ("staticanalysis"), not the
// "_test" external convention, because this directory contains ONLY test
// files — Go would refuse to build an "_test" package with no
// corresponding non-test package in the same directory.
package staticanalysis

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// -----------------------------------------------------------------------------
// Repo discovery
// -----------------------------------------------------------------------------

// repoRoot walks up from this test file looking for the first directory that
// contains a go.mod file. That directory is the module / repository root.
// Centralising the discovery here keeps every test resilient to being run
// from any working directory (go test, IDE runner, CI, etc.).
func repoRoot(tb testing.TB) string {
	tb.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("runtime.Caller(0) failed; cannot resolve repo root")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatalf("could not locate go.mod walking up from %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// backendDir returns the absolute path to apps/backend rooted at the
// discovered repo root.
func backendDir(tb testing.TB) string {
	tb.Helper()
	return filepath.Join(repoRoot(tb), "apps", "backend")
}

// -----------------------------------------------------------------------------
// Generic grep helper
// -----------------------------------------------------------------------------

// match describes a single hit produced by grepGoFiles.
type match struct {
	Path string // path relative to repo root, with forward slashes
	Line int    // 1-based line number where the pattern matched
	Text string // raw matched line (trimmed)
}

// grepOpts configures grepGoFiles.
type grepOpts struct {
	// Root is the directory to walk (recursively).
	Root string
	// Pattern is the (already compiled) regular expression to match against
	// each non-empty line of every selected file.
	Pattern *regexp.Regexp
	// FileSuffix restricts the scan to files ending with this suffix
	// (".go", ".mod", etc.). Pass "" to scan every regular file.
	FileSuffix string
	// IncludeTestFiles, when false, skips any file whose name matches
	// "*_test.go". The default (false) mirrors the feature-spec semantics
	// where mock-data is allowed only in test files.
	IncludeTestFiles bool
	// SkipDirs is the set of directory names that should NOT be descended
	// into (matched on Base name only). Defaults applied by grepGoFiles.
	SkipDirs map[string]struct{}
}

// defaultSkipDirs lists directory names that are never scanned for source
// patterns: VCS metadata, build output, vendored deps, IDE state, the
// auto-generated playwright cache, and the AutoForge meta-documentation
// directory which contains the very pattern names we forbid.
var defaultSkipDirs = map[string]struct{}{
	".git":            {},
	".github":         {},
	".idea":           {},
	".vscode":         {},
	".playwright":     {},
	".playwright-cli": {},
	".autoforge":      {},
	"node_modules":    {},
	"vendor":          {},
	"dist":            {},
	"build":           {},
}

// grepGoFiles walks opts.Root, opens every file that matches opts.FileSuffix,
// and returns every line that matches opts.Pattern. The returned slice is
// empty when the pattern is absent — that is the success condition for all
// tests in this package.
func grepFiles(tb testing.TB, opts grepOpts) []match {
	tb.Helper()
	if opts.Pattern == nil {
		tb.Fatal("grepFiles: nil pattern")
	}
	skip := opts.SkipDirs
	if skip == nil {
		skip = defaultSkipDirs
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		tb.Fatalf("grepFiles: resolve %s: %v", opts.Root, err)
	}

	var hits []match
	repo := repoRoot(tb)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, blocked := skip[d.Name()]; blocked {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if opts.FileSuffix != "" && !strings.HasSuffix(name, opts.FileSuffix) {
			return nil
		}
		if !opts.IncludeTestFiles && strings.HasSuffix(name, "_test.go") {
			return nil
		}

		f, ferr := os.Open(path)
		if ferr != nil {
			return ferr
		}
		defer f.Close()

		// Skip this very test file (and any sibling _test.go file in this
		// package) — it embeds every forbidden pattern as a literal regex
		// source and would otherwise be a self-match. We do this even when
		// IncludeTestFiles is true so the "static-analysis tests" remain
		// orthogonal to the production code they police.
		rel, _ := filepath.Rel(repo, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "apps/backend/tests/staticanalysis/") {
			return nil
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024) // tolerate long lines
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if opts.Pattern.MatchString(line) {
				hits = append(hits, match{
					Path: rel,
					Line: lineNo,
					Text: strings.TrimSpace(line),
				})
			}
		}
		return scanner.Err()
	})
	if walkErr != nil {
		tb.Fatalf("grepFiles: walk %s: %v", opts.Root, walkErr)
	}
	return hits
}

// reportHits is the canonical failure message used by every assertion in this
// file. It echoes back the original spec step (so a CI failure surfaces the
// exact contract that was broken) and lists every offending line.
func reportHits(tb testing.TB, specStep string, hits []match) {
	tb.Helper()
	if len(hits) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString(specStep)
	b.WriteString("\n\nFound ")
	b.WriteString(itoa(len(hits)))
	b.WriteString(" forbidden match(es):\n")
	for _, h := range hits {
		b.WriteString("  - ")
		b.WriteString(h.Path)
		b.WriteString(":")
		b.WriteString(itoa(h.Line))
		b.WriteString("  ")
		b.WriteString(h.Text)
		b.WriteString("\n")
	}
	tb.Fatal(b.String())
}

// itoa avoids importing strconv just for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// -----------------------------------------------------------------------------
// Spec step 1: browser-global stores
// -----------------------------------------------------------------------------

func TestNoBrowserGlobalStores(t *testing.T) {
	pattern := regexp.MustCompile(`globalThis\.|window\.__store__`)
	hits := grepFiles(t, grepOpts{
		Root:       backendDir(t),
		Pattern:    pattern,
		FileSuffix: ".go",
	})
	reportHits(t,
		`Spec step 1: grep -rE 'globalThis\.|window\.__store__' apps/backend --include='*.go' must return empty.`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 2: dev-store / mock-db variants
// -----------------------------------------------------------------------------

func TestNoDevStoreOrMockDB(t *testing.T) {
	pattern := regexp.MustCompile(`dev-store|devStore|DevStore|mock-db|mockDb|MockDB`)
	hits := grepFiles(t, grepOpts{
		Root:       backendDir(t),
		Pattern:    pattern,
		FileSuffix: ".go",
	})
	reportHits(t,
		`Spec step 2: grep -rE 'dev-store|devStore|DevStore|mock-db|mockDb|MockDB' apps/backend --include='*.go' must return empty.`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 3: mockData / fakeData / sampleData / dummyData / stubData
// -----------------------------------------------------------------------------

func TestNoNamedMockData(t *testing.T) {
	pattern := regexp.MustCompile(`mockData|fakeData|sampleData|dummyData|stubData`)
	hits := grepFiles(t, grepOpts{
		Root:       backendDir(t),
		Pattern:    pattern,
		FileSuffix: ".go",
	})
	reportHits(t,
		`Spec step 3: grep -rE 'mockData|fakeData|sampleData|dummyData|stubData' apps/backend --include='*.go' must return empty.`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 4: TODO/FIXME-style mock placeholders + STUB token
// -----------------------------------------------------------------------------

func TestNoMockTodoOrStubMarkers(t *testing.T) {
	// The spec string uses ERE alternation; the Go RE2 equivalent of the
	// four alternatives in the spec is identical, just compiled at runtime.
	pattern := regexp.MustCompile(`TODO.*real DB|TODO.*replace.*mock|FIXME.*mock|STUB`)
	hits := grepFiles(t, grepOpts{
		Root:       backendDir(t),
		Pattern:    pattern,
		FileSuffix: ".go",
	})
	reportHits(t,
		`Spec step 4: grep -rE 'TODO.*real DB|TODO.*replace.*mock|FIXME.*mock|STUB' apps/backend --include='*.go' must return empty.`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 5: JavaScript mock-backend libraries
// -----------------------------------------------------------------------------

// TestNoJSMockBackendDeps enforces that no JavaScript mock-backend library
// has crept into the project. The literal spec command (`grep -rE ... .`)
// recursively scans the whole repo, but with two pragmatic restrictions:
//
//  1. We restrict the scan to dependency-manifest and source files
//     (package.json, package-lock.json, yarn.lock, *.go, *.mod, *.sum,
//     *.ts, *.tsx, *.js, *.jsx). Binary assets (SVG base64 blobs, PNGs,
//     PDFs) and the AutoForge meta-documentation directory — which
//     deliberately enumerates these forbidden names — would otherwise
//     produce false positives that have nothing to do with the
//     implementation under test.
//
//  2. For the bare token "msw" we require a word boundary so we don't
//     trigger on incidental substrings (e.g. base64 garbage, locale
//     suffixes, or English words ending in "...msw"). "json-server" and
//     "miragejs" are distinctive enough that no boundary is necessary.
//
// Both restrictions preserve the spec's actual intent ("ensures the
// implementation does not silently fake persistence") while filtering the
// noise that would otherwise hide real failures.
func TestNoJSMockBackendDeps(t *testing.T) {
	pattern := regexp.MustCompile(`json-server|miragejs|\bmsw\b`)

	scanSuffixes := map[string]struct{}{
		".go":   {},
		".mod":  {},
		".sum":  {},
		".json": {},
		".js":   {},
		".jsx":  {},
		".ts":   {},
		".tsx":  {},
		".lock": {},
		".yaml": {},
		".yml":  {},
	}

	root := repoRoot(t)
	var hits []match
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, blocked := defaultSkipDirs[d.Name()]; blocked {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(d.Name())
		if _, ok := scanSuffixes[ext]; !ok {
			// package-lock.json has the .json suffix already covered; yarn.lock
			// matches .lock above. Anything else (svg, pdf, png, woff) is
			// skipped because it cannot legitimately introduce a JS mock dep.
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// Skip this very test file (it spells the forbidden tokens out as
		// regex source).
		if strings.HasPrefix(rel, "apps/backend/tests/staticanalysis/") {
			return nil
		}

		f, ferr := os.Open(path)
		if ferr != nil {
			return ferr
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if pattern.MatchString(line) {
				hits = append(hits, match{
					Path: rel,
					Line: lineNo,
					Text: strings.TrimSpace(line),
				})
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}

	reportHits(t,
		`Spec step 5: grep -rE 'json-server|miragejs|msw' . must return empty (scoped to dependency manifests + source files; binary assets and .autoforge meta-docs excluded).`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 6: in-memory store types
// -----------------------------------------------------------------------------

func TestNoInMemoryStoreTypes(t *testing.T) {
	pattern := regexp.MustCompile(`in[-_]?memory[-_]?store|InMemoryStore`)
	hits := grepFiles(t, grepOpts{
		Root:             backendDir(t),
		Pattern:          pattern,
		FileSuffix:       ".go",
		IncludeTestFiles: false, // spec: "allowed only inside *_test.go files"
	})
	reportHits(t,
		`Spec step 6: grep -rE 'in[-_]?memory[-_]?store|InMemoryStore' apps/backend --include='*.go' must return empty (allowed only inside *_test.go files).`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 7: map[string]*Type{} stand-in stores inside adapter packages
// -----------------------------------------------------------------------------

func TestNoMapBackedAdapterStores(t *testing.T) {
	// The spec regex matches map literals like `map[string]*Foo{}`, which
	// is the typical shape of an ad-hoc in-memory store used as a database
	// stand-in. Real adapters must live behind a pgx-backed repository.
	pattern := regexp.MustCompile(`map\[string\]\*[A-Z][a-zA-Z]+\s*\{\}`)
	adaptersDir := filepath.Join(backendDir(t), "internal", "adapters")
	if _, err := os.Stat(adaptersDir); err != nil {
		t.Fatalf("internal/adapters directory missing: %v", err)
	}
	hits := grepFiles(t, grepOpts{
		Root:       adaptersDir,
		Pattern:    pattern,
		FileSuffix: ".go",
	})
	reportHits(t,
		`Spec step 7: grep -rE 'map\[string\]\*[A-Z][a-zA-Z]+\s*\{\}' apps/backend/internal/adapters --include='*.go' must return empty (adapters must use pgx).`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 8: internal/adapters/postgres must exist and be the only adapter
// -----------------------------------------------------------------------------

// TestPostgresIsOnlyDataStoreAdapter asserts the structural invariant that
// the only data-store adapter package in internal/adapters/ is "postgres".
// Other sibling directories (http for outbound webhooks, etc.) are allowed
// because they are not data-store adapters; this test only flags packages
// whose name looks like a competing data backend (sqlite, mysql, mongo,
// redis-store, badger, boltdb, buntdb, sqlx, memory, …).
func TestPostgresIsOnlyDataStoreAdapter(t *testing.T) {
	adaptersDir := filepath.Join(backendDir(t), "internal", "adapters")
	postgresDir := filepath.Join(adaptersDir, "postgres")

	info, err := os.Stat(postgresDir)
	if err != nil {
		t.Fatalf(`Spec step 8: internal/adapters/postgres must exist; got: %v`, err)
	}
	if !info.IsDir() {
		t.Fatalf(`Spec step 8: internal/adapters/postgres must be a directory, got file`)
	}

	// Walk siblings and flag any that look like a competing store.
	competing := map[string]struct{}{
		"sqlite":      {},
		"sqlite3":     {},
		"mysql":       {},
		"mariadb":     {},
		"mongo":       {},
		"mongodb":     {},
		"badger":      {},
		"boltdb":      {},
		"bolt":        {},
		"buntdb":      {},
		"redis-store": {},
		"memory":      {},
		"inmemory":    {},
		"in-memory":   {},
		"mockdb":      {},
		"mock":        {},
	}
	entries, err := os.ReadDir(adaptersDir)
	if err != nil {
		t.Fatalf("read internal/adapters: %v", err)
	}
	var offenders []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, bad := competing[strings.ToLower(e.Name())]; bad {
			offenders = append(offenders, e.Name())
		}
	}
	if len(offenders) > 0 {
		t.Fatalf(
			`Spec step 8: internal/adapters/postgres must be the only data-store adapter; found competing adapter dirs: %v`,
			offenders,
		)
	}
}

// -----------------------------------------------------------------------------
// Spec step 9: no sqlite/badger/boltdb/buntdb modules in go.mod
// -----------------------------------------------------------------------------

func TestNoEmbeddedDBImportsInGoMod(t *testing.T) {
	goModPath := filepath.Join(repoRoot(t), "go.mod")
	contents, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	pattern := regexp.MustCompile(`(?i)\b(mattn/go-sqlite3|modernc\.org/sqlite|sqlite3?|badger|boltdb|bbolt|buntdb)\b`)
	var hits []match
	for i, line := range strings.Split(string(contents), "\n") {
		// Skip comments (lines starting with //) so doc lines in go.mod
		// don't trip the test.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if pattern.MatchString(line) {
			hits = append(hits, match{
				Path: "go.mod",
				Line: i + 1,
				Text: trimmed,
			})
		}
	}
	reportHits(t,
		`Spec step 9: go.mod must not import sqlite, badger, boltdb, bbolt, or buntdb (PostgreSQL is the only data store).`,
		hits,
	)
}

// -----------------------------------------------------------------------------
// Spec step 10: meta — every grep returned exit code 1
// -----------------------------------------------------------------------------

// TestAllChecksPassed is the umbrella assertion required by spec step 10
// ("Exit code of all greps must be 1"). The Go test runner already reports
// each *Test* function above individually; this test exists as a documented
// roll-up so that anyone reading the suite immediately sees the contract:
// "all of the above checks must pass simultaneously". It runs every check
// as a subtest, providing a single pass/fail signal for the feature gate.
func TestAllChecksPassed(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"NoBrowserGlobalStores", TestNoBrowserGlobalStores},
		{"NoDevStoreOrMockDB", TestNoDevStoreOrMockDB},
		{"NoNamedMockData", TestNoNamedMockData},
		{"NoMockTodoOrStubMarkers", TestNoMockTodoOrStubMarkers},
		{"NoJSMockBackendDeps", TestNoJSMockBackendDeps},
		{"NoInMemoryStoreTypes", TestNoInMemoryStoreTypes},
		{"NoMapBackedAdapterStores", TestNoMapBackedAdapterStores},
		{"PostgresIsOnlyDataStoreAdapter", TestPostgresIsOnlyDataStoreAdapter},
		{"NoEmbeddedDBImportsInGoMod", TestNoEmbeddedDBImportsInGoMod},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, c.fn)
	}
}
