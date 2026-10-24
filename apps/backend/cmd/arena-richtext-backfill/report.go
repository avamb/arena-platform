package main

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// maxShown bounds how much of a text the report prints with --show-text; the
// whole old value is in richtext_backfill_backup.
const maxShown = 600

func shorten(s string) string {
	r := []rune(s)
	if len(r) <= maxShown {
		return s
	}
	return string(r[:maxShown]) + fmt.Sprintf("… [%d more characters]", len(r)-maxShown)
}

func localeLabel(l string) string {
	if l == "" {
		return "base"
	}
	return l
}

// fence puts text in a code block whose fence is longer than any run of
// backticks in the text.
func fence(text string) string {
	f := "```"
	for strings.Contains(text, f) {
		f += "`"
	}
	return f + "\n" + shorten(text) + "\n" + f + "\n"
}

// Markdown renders the report for the owner. By default it holds ids,
// locales, lengths and reasons only: the texts of events and notes can carry
// phone numbers and addresses, and a report on stdout ends up in the logs of
// the pod. The texts are in it only when the run was asked to show them
// (--show-text, which writes only into a --report file of mode 0600).
func (r *Report) Markdown() string {
	var b strings.Builder
	title := "Rich-text backfill"
	if r.Rollback {
		title = "Rich-text backfill rollback"
	}
	if r.Purge != nil {
		title = "Rich-text backfill backup purge"
	}
	mode := "DRY RUN (nothing was written)"
	if r.Apply {
		mode = "APPLIED"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- mode: %s\n", mode)
	if r.Purge != nil {
		what := "would be deleted"
		if r.Apply {
			what = "deleted"
		}
		fmt.Fprintf(&b, "- backup rows older than %d days %s: %d\n", r.Purge.Days, what, r.Purge.Rows)
		return b.String()
	}
	if r.Apply || r.Rollback {
		fmt.Fprintf(&b, "- run id: `%s`\n", r.RunID)
	}
	fmt.Fprintf(&b, "- events looked at: %d\n- email notes looked at: %d\n", r.EventsScanned, r.NotesScanned)
	fmt.Fprintf(&b, "- texts changed: %d\n- texts left as they are: %d\n", len(r.Changed), len(r.Skipped))
	if r.NextAfter != uuid.Nil {
		fmt.Fprintf(&b, "- the limit stopped the run: continue with `--after %s`\n", r.NextAfter)
	}
	if r.ShowText {
		b.WriteString("- texts are shown (--show-text)\n")
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "## Left as they are (%d)\n\n", len(r.Skipped))
	if len(r.Skipped) == 0 {
		b.WriteString("None.\n\n")
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "- `%s` %s locale=%s: **%s**", s.Entity, s.ID, localeLabel(s.Locale), s.Reason)
		if s.Detail != "" {
			fmt.Fprintf(&b, " (%s)", s.Detail)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "## Lines that start with a single \"-\" or \"*\" (%d)\n\n", len(r.SingleMarkers))
	b.WriteString("A block of one line is not a list: the mark stays in the text.\n\n")
	writeNotices(&b, r.SingleMarkers)

	fmt.Fprintf(&b, "## Texts with a literal \"<...>\" (%d)\n\n", len(r.LiteralTags))
	b.WriteString("Read as the old text the tags stay visible as they were written; they are no longer markup.\n\n")
	writeNotices(&b, r.LiteralTags)

	label := "Changed"
	if r.Rollback {
		label = "Restored"
	}
	if !r.Apply {
		label = "Would be " + strings.ToLower(label)
	}
	fmt.Fprintf(&b, "## %s (%d)\n\n", label, len(r.Changed))
	for _, c := range r.Changed {
		if !r.ShowText {
			fmt.Fprintf(&b, "- `%s` %s locale=%s: %d → %d characters\n", c.Entity, c.ID, localeLabel(c.Locale), c.BeforeLen, c.AfterLen)
			continue
		}
		fmt.Fprintf(&b, "### `%s` %s locale=%s\n\nBefore:\n\n%s\nAfter:\n\n%s\n", c.Entity, c.ID, localeLabel(c.Locale), fence(c.Before), fence(c.After))
	}
	return b.String()
}

func writeNotices(b *strings.Builder, rows []noticeRow) {
	for _, n := range rows {
		if n.Detail == "" {
			fmt.Fprintf(b, "- `%s` %s locale=%s: %d characters\n", n.Entity, n.ID, localeLabel(n.Locale), n.Len)
			continue
		}
		fmt.Fprintf(b, "- `%s` %s locale=%s: `%s`\n", n.Entity, n.ID, localeLabel(n.Locale), strings.ReplaceAll(shorten(n.Detail), "`", "'"))
	}
	b.WriteString("\n")
}
