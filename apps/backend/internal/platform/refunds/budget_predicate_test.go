package refunds

import (
	"regexp"
	"strings"
	"testing"
)

// normalizePredicate drops the table alias and the whitespace, so the
// aliased hand copies of the budget rule compare with the unqualified one.
func normalizePredicate(sql, alias string) string {
	if alias != "" {
		sql = strings.ReplaceAll(sql, alias+".", "")
	}
	return regexp.MustCompile(`\s+`).ReplaceAllString(sql, "")
}

// The budget rule is written out by hand where the refunds row has an alias
// (the queue's "retry is blocked" flag, the review-hold clear). Each copy must
// say exactly what countsAgainstBudgetSQL says: the rule has changed several
// times, and a copy that drifts makes the screen disagree with the engine.
func TestBudgetPredicate_HandCopiesMatchTheEngine(t *testing.T) {
	want := normalizePredicate(countsAgainstBudgetSQL, "")
	live := normalizePredicate(`state NOT IN ('failed', 'rejected') AND `, "")
	for name, copy := range map[string]string{
		"list.go liveAgainstBudget":           normalizePredicate(liveAgainstBudget, "o"),
		"unattributed.go unattributedLiveSQL": normalizePredicate(unattributedLiveSQL, "r"),
	} {
		if copy != live+want {
			t.Errorf("%s drifted from countsAgainstBudgetSQL:\n got %s\nwant %s", name, copy, live+want)
		}
	}
	for _, part := range []string{"providerISNOTNULL", failureReversedAfterSuccess, failureFailedAfterAcceptance} {
		if !strings.Contains(want, part) {
			t.Errorf("countsAgainstBudgetSQL lacks %q", part)
		}
	}
}
