package main

import (
	"fmt"
	"strings"
)

// maxShown bounds how much of a text the report prints; the whole old value is
// in richtext_backfill_backup.
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

// Markdown renders the report for the owner.
func (r *Report) Markdown() string {
	var b strings.Builder
	title := "Rich-text backfill"
	if r.Rollback {
		title = "Rich-text backfill rollback"
	}
	mode := "DRY RUN (nothing was written)"
	if r.Apply {
		mode = "APPLIED"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- mode: %s\n", mode)
	if r.Apply || r.Rollback {
		fmt.Fprintf(&b, "- run id: `%s`\n", r.RunID)
	}
	fmt.Fprintf(&b, "- events looked at: %d\n- email notes looked at: %d\n", r.EventsScanned, r.NotesScanned)
	fmt.Fprintf(&b, "- texts changed: %d\n- texts left as they are: %d\n\n", len(r.Changed), len(r.Skipped))

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
	for _, n := range r.SingleMarkers {
		fmt.Fprintf(&b, "- `%s` %s locale=%s: `%s`\n", n.Entity, n.ID, localeLabel(n.Locale), strings.ReplaceAll(shorten(n.Detail), "`", "'"))
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "## Texts with a literal \"<...>\" (%d)\n\n", len(r.LiteralTags))
	b.WriteString("The text stays visible as it was written; it is no longer markup.\n\n")
	for _, n := range r.LiteralTags {
		fmt.Fprintf(&b, "- `%s` %s locale=%s: `%s`\n", n.Entity, n.ID, localeLabel(n.Locale), strings.ReplaceAll(shorten(n.Detail), "`", "'"))
	}
	b.WriteString("\n")

	label := "Changed"
	if r.Rollback {
		label = "Restored"
	}
	if !r.Apply {
		label = "Would be " + strings.ToLower(label)
	}
	fmt.Fprintf(&b, "## %s (%d)\n\n", label, len(r.Changed))
	for _, c := range r.Changed {
		fmt.Fprintf(&b, "### `%s` %s locale=%s\n\nBefore:\n\n%s\nAfter:\n\n%s\n", c.Entity, c.ID, localeLabel(c.Locale), fence(c.Before), fence(c.After))
	}
	return b.String()
}
