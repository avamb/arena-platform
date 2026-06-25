// wcag_accessibility_165_test.go — unit tests for feature #165
// (WCAG 2.2 AA accessibility audit — checkout + public storefronts).
//
// Test coverage:
//
//	Step 1:  class-checkout.php has aria-label on checkout forms
//	Step 2:  class-checkout.php has <label for="..."> elements for inputs
//	Step 3:  class-checkout.php has aria-required="true" on email input
//	Step 4:  class-checkout.php has aria-live="assertive" on error region
//	Step 5:  class-checkout.php has role="alert" on error region
//	Step 6:  class-checkout.php has aria-atomic="true" on error region
//	Step 7:  class-checkout.php has autocomplete="email" on email input
//	Step 8:  class-checkout.php has aria-describedby linking inputs to error div
//	Step 9:  class-checkout.php has visible focus indicator (outline) in CSS
//	Step 10: class-checkout.php has novalidate on form (defers to JS/screen reader)
//	Step 11: class-checkout.php error div has unique id per tier (data-error-id)
//	Step 12: class-checkout.php JS clears error region on new submission
//	Step 13: class-checkout.php JS sets aria-busy="true" on loading button
//	Step 14: class-checkout.php JS removes aria-busy on completion
//	Step 15: class-checkout.php sold-out state renders accessible text label
//	Step 16: class-checkout.php uses #595959 (≥4.5:1) for availability text
//	Step 17: class-checkout.php uses #c00 on white (≥4.5:1) for error/sold-out
//	Step 18: class-checkout.php uses #005fcc for focus outline (≥3.0:1)
//	Step 19: wcag-checklist.md exists and covers WCAG 2.2 AA
//	Step 20: wcag-checklist.md references 1.4.3 colour contrast criterion
//	Step 21: wcag-checklist.md references 2.4.7 focus visible criterion
//	Step 22: wcag-checklist.md references 4.1.3 status messages criterion
//	Step 23: wp-demo-test-plan.md exists and is signed off
//	Step 24: wp-demo-test-plan.md covers keyboard navigation test
//	Step 25: wp-demo-test-plan.md covers screen reader (NVDA) test
//	Step 26: wp-demo-test-plan.md covers colour contrast table
//	Step 27: remediation-backlog.md exists with open items
//	Step 28: remediation-backlog.md documents RB-001 border contrast issue
//	Step 29: remediation-backlog.md documents RB-002 session timer
//	Step 30: remediation-backlog.md documents RB-004 button target size
//	Step 31: accessibility.yml CI workflow exists with axe-core reference
//	Step 32: accessibility.yml includes WCAG 2.2 AA tags
//	Step 33: accessibility.yml checks for critical violations
//	Step 34: accessibility.yml includes aria-lint job
//	Step 35: generate-snapshots.js exists with HTML snapshots output
//	Step 36: generate-snapshots.js generates checkout-tiers.html
//	Step 37: generate-snapshots.js generates checkout-sold-out.html
//	Step 38: generate-snapshots.js generates checkout-error-state.html
//	Step 39: checkout form has .arena-field wrapper with label+input
//	Step 40: class-checkout.php form has unique id per tier
//
// All tests are pure file/content checks — no live WordPress or browser required.
package httpserver

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// repoRootForA11y locates the repo root (directory containing go.mod).
func repoRootForA11y(t *testing.T) string {
	t.Helper()

	// Strategy 1: runtime.Caller absolute path.
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		dir := filepath.Dir(thisFile)
		root := dir
		for i := 0; i < 5; i++ {
			root = filepath.Dir(root)
		}
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
	}

	// Strategy 2: walk upward from CWD.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("repoRootForA11y: cannot determine CWD: %v", err)
	}
	dir := cwd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("repoRootForA11y: cannot locate repo root; cwd=%s", cwd)
	return ""
}

// readA11yFile reads a file relative to the repo root and returns its content.
func readA11yFile(t *testing.T, relPath string) string {
	t.Helper()
	root := repoRootForA11y(t)
	full := filepath.Join(root, filepath.FromSlash(relPath))
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("readA11yFile(%q): %v", relPath, err)
	}
	return string(data)
}

// a11yAssertContains fails if src does not contain substr.
func a11yAssertContains(t *testing.T, src, substr, label string) {
	t.Helper()
	if !strings.Contains(src, substr) {
		t.Errorf("%s: expected to contain %q", label, substr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: class-checkout.php has aria-label on checkout forms
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step01_AriaLabelOnForm(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `aria-label="`, "class-checkout.php Step 1")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: class-checkout.php has <label for="..."> elements
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step02_LabelForAttribute(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `for="`, "class-checkout.php Step 2")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: class-checkout.php has aria-required="true" on email input
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step03_AriaRequired(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `aria-required="true"`, "class-checkout.php Step 3")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: class-checkout.php has aria-live="assertive" on error region
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step04_AriaLiveAssertive(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `aria-live="assertive"`, "class-checkout.php Step 4")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: class-checkout.php has role="alert" on error region
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step05_RoleAlert(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `role="alert"`, "class-checkout.php Step 5")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: class-checkout.php has aria-atomic="true" on error region
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step06_AriaAtomic(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `aria-atomic="true"`, "class-checkout.php Step 6")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: class-checkout.php has autocomplete="email" on email input
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step07_AutocompleteEmail(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `autocomplete="email"`, "class-checkout.php Step 7")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: class-checkout.php has aria-describedby linking inputs to error div
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step08_AriaDescribedBy(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `aria-describedby="`, "class-checkout.php Step 8")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: class-checkout.php has visible focus indicator (outline) in CSS
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step09_FocusOutlineInCSS(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `outline:2px solid`, "class-checkout.php Step 9")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: class-checkout.php form has novalidate (defers to JS validation)
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step10_NoValidate(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `novalidate`, "class-checkout.php Step 10")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: class-checkout.php error div has unique id per tier (data-error-id)
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step11_ErrorDivUniqueID(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `data-error-id="`, "class-checkout.php Step 11 data-error-id attr")
	a11yAssertContains(t, src, `$err_id`, "class-checkout.php Step 11 err_id variable")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: class-checkout.php JS clears error region on new submission
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step12_JSClearsErrorOnSubmit(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	// JS should empty the error element before making the fetch call.
	a11yAssertContains(t, src, `errEl.textContent=""`, "class-checkout.php Step 12")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 13: class-checkout.php JS sets aria-busy="true" on loading button
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step13_AriaBusyOnLoading(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `aria-busy`, "class-checkout.php Step 13")
	a11yAssertContains(t, src, `setAttribute("aria-busy","true")`, "class-checkout.php Step 13 setAttribute")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 14: class-checkout.php JS removes aria-busy on completion
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step14_AriaBusyRemovedOnDone(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `removeAttribute("aria-busy")`, "class-checkout.php Step 14")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 15: class-checkout.php sold-out state renders accessible text label
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step15_SoldOutTextLabel(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `sold-out`, "class-checkout.php Step 15 CSS class")
	a11yAssertContains(t, src, `Sold Out`, "class-checkout.php Step 15 text label")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 16: class-checkout.php uses #595959 for availability text (7:1 contrast)
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step16_ContrastAvailabilityColour(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `#595959`, "class-checkout.php Step 16 availability colour")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 17: class-checkout.php uses #c00 for error/sold-out text (5.9:1)
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step17_ContrastErrorColour(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `#c00`, "class-checkout.php Step 17 error colour")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 18: class-checkout.php uses #005fcc for focus outline (8.6:1 on white)
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step18_FocusOutlineColour(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `#005fcc`, "class-checkout.php Step 18 focus outline colour")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 19: wcag-checklist.md exists and covers WCAG 2.2 AA
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step19_ChecklistExists(t *testing.T) {
	root := repoRootForA11y(t)
	path := filepath.Join(root, "ops", "accessibility", "wcag-checklist.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("wcag-checklist.md does not exist: %v", err)
	}
	src := readA11yFile(t, "ops/accessibility/wcag-checklist.md")
	a11yAssertContains(t, src, "WCAG 2.2", "wcag-checklist.md Step 19")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 20: wcag-checklist.md references 1.4.3 colour contrast criterion
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step20_ChecklistColourContrast(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/wcag-checklist.md")
	a11yAssertContains(t, src, "1.4.3", "wcag-checklist.md Step 20 criterion 1.4.3")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 21: wcag-checklist.md references 2.4.7 focus visible criterion
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step21_ChecklistFocusVisible(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/wcag-checklist.md")
	a11yAssertContains(t, src, "2.4.7", "wcag-checklist.md Step 21 criterion 2.4.7")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 22: wcag-checklist.md references 4.1.3 status messages criterion
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step22_ChecklistStatusMessages(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/wcag-checklist.md")
	a11yAssertContains(t, src, "4.1.3", "wcag-checklist.md Step 22 criterion 4.1.3")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 23: wp-demo-test-plan.md exists and is signed off
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step23_TestPlanExists(t *testing.T) {
	root := repoRootForA11y(t)
	path := filepath.Join(root, "ops", "accessibility", "wp-demo-test-plan.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("wp-demo-test-plan.md does not exist: %v", err)
	}
	src := readA11yFile(t, "ops/accessibility/wp-demo-test-plan.md")
	a11yAssertContains(t, src, "Signed off", "wp-demo-test-plan.md Step 23 sign-off")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 24: wp-demo-test-plan.md covers keyboard navigation test
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step24_TestPlanKeyboard(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/wp-demo-test-plan.md")
	a11yAssertContains(t, src, "Keyboard", "wp-demo-test-plan.md Step 24")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 25: wp-demo-test-plan.md covers screen reader (NVDA) test
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step25_TestPlanScreenReader(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/wp-demo-test-plan.md")
	a11yAssertContains(t, src, "NVDA", "wp-demo-test-plan.md Step 25")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 26: wp-demo-test-plan.md covers colour contrast table
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step26_TestPlanColourContrast(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/wp-demo-test-plan.md")
	a11yAssertContains(t, src, "Colour Contrast", "wp-demo-test-plan.md Step 26")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 27: remediation-backlog.md exists with open items
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step27_RemediationBacklogExists(t *testing.T) {
	root := repoRootForA11y(t)
	path := filepath.Join(root, "ops", "accessibility", "remediation-backlog.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("remediation-backlog.md does not exist: %v", err)
	}
	src := readA11yFile(t, "ops/accessibility/remediation-backlog.md")
	a11yAssertContains(t, src, "Open Items", "remediation-backlog.md Step 27")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 28: remediation-backlog.md documents RB-001 border contrast issue
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step28_RemediationRB001BorderContrast(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/remediation-backlog.md")
	a11yAssertContains(t, src, "RB-001", "remediation-backlog.md Step 28 RB-001")
	a11yAssertContains(t, src, "1.4.11", "remediation-backlog.md Step 28 criterion 1.4.11")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 29: remediation-backlog.md documents RB-002 session timer
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step29_RemediationRB002Timer(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/remediation-backlog.md")
	a11yAssertContains(t, src, "RB-002", "remediation-backlog.md Step 29 RB-002")
	a11yAssertContains(t, src, "2.2.1", "remediation-backlog.md Step 29 criterion 2.2.1")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 30: remediation-backlog.md documents RB-004 button target size
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step30_RemediationRB004TargetSize(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/remediation-backlog.md")
	a11yAssertContains(t, src, "RB-004", "remediation-backlog.md Step 30 RB-004")
	a11yAssertContains(t, src, "2.5.8", "remediation-backlog.md Step 30 criterion 2.5.8")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 31: accessibility.yml CI workflow exists with axe-core reference
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step31_CIWorkflowExists(t *testing.T) {
	root := repoRootForA11y(t)
	path := filepath.Join(root, ".github", "workflows", "accessibility.yml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("accessibility.yml does not exist: %v", err)
	}
	src := readA11yFile(t, ".github/workflows/accessibility.yml")
	a11yAssertContains(t, src, "axe-core", "accessibility.yml Step 31")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 32: accessibility.yml includes WCAG 2.2 AA tags in axe invocation
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step32_CIWorkflowWCAG22Tags(t *testing.T) {
	src := readA11yFile(t, ".github/workflows/accessibility.yml")
	a11yAssertContains(t, src, "wcag22aa", "accessibility.yml Step 32 wcag22aa tag")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 33: accessibility.yml checks for critical violations and fails CI
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step33_CIWorkflowViolationCheck(t *testing.T) {
	src := readA11yFile(t, ".github/workflows/accessibility.yml")
	a11yAssertContains(t, src, "critical", "accessibility.yml Step 33 critical check")
	a11yAssertContains(t, src, "process.exit(1)", "accessibility.yml Step 33 fail on violation")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 34: accessibility.yml includes aria-lint job
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step34_CIWorkflowAriaLintJob(t *testing.T) {
	src := readA11yFile(t, ".github/workflows/accessibility.yml")
	a11yAssertContains(t, src, "aria-lint", "accessibility.yml Step 34 aria-lint job")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 35: generate-snapshots.js exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step35_SnapshotGeneratorExists(t *testing.T) {
	root := repoRootForA11y(t)
	path := filepath.Join(root, "ops", "accessibility", "generate-snapshots.js")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("generate-snapshots.js does not exist: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 36: generate-snapshots.js generates checkout-tiers.html
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step36_SnapshotCheckoutTiers(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/generate-snapshots.js")
	a11yAssertContains(t, src, "checkout-tiers.html", "generate-snapshots.js Step 36")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 37: generate-snapshots.js generates checkout-sold-out.html
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step37_SnapshotSoldOut(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/generate-snapshots.js")
	a11yAssertContains(t, src, "checkout-sold-out.html", "generate-snapshots.js Step 37")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 38: generate-snapshots.js generates checkout-error-state.html
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step38_SnapshotErrorState(t *testing.T) {
	src := readA11yFile(t, "ops/accessibility/generate-snapshots.js")
	a11yAssertContains(t, src, "checkout-error-state.html", "generate-snapshots.js Step 38")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 39: class-checkout.php has .arena-field wrapper with label+input group
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step39_FieldWrapperWithLabel(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `arena-field`, "class-checkout.php Step 39 arena-field class")
	a11yAssertContains(t, src, `<label`, "class-checkout.php Step 39 label element")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 40: class-checkout.php form has unique id per tier ($form_id)
// ─────────────────────────────────────────────────────────────────────────────

func TestWCAGAccessibility165_Step40_FormUniqueID(t *testing.T) {
	src := readA11yFile(t, "apps/wp-plugin/arena-events/includes/class-checkout.php")
	a11yAssertContains(t, src, `$form_id`, "class-checkout.php Step 40 form_id variable")
	a11yAssertContains(t, src, `id="' . $form_id . '"`, "class-checkout.php Step 40 form id attribute")
}
