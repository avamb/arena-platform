package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// FieldTotal is the matches of one field over a spec's images.
type FieldTotal struct {
	Matched int `json:"matched"`
	Scored  int `json:"scored"`
}

// SpecSummary is the roll-up of one model over all images.
type SpecSummary struct {
	Spec     string                `json:"spec"`
	Images   int                   `json:"images"`
	Errors   int                   `json:"errors"`
	AvgMs    int64                 `json:"avg_ms"`
	MaxMs    int64                 `json:"max_ms"`
	Matched  int                   `json:"matched"`
	Scored   int                   `json:"scored"`
	Accuracy float64               `json:"accuracy"`
	Fields   map[string]FieldTotal `json:"fields"`
}

// Report is what the command writes as JSON.
type Report struct {
	GeneratedAt string        `json:"generated_at"`
	Summary     []SpecSummary `json:"summary"`
	Runs        []Run         `json:"runs"`
}

// Summarize rolls the runs up per spec, in the order of specs. The latency
// average covers the successful reads only.
func Summarize(specs []Spec, runs []Run) []SpecSummary {
	out := make([]SpecSummary, 0, len(specs))
	for _, sp := range specs {
		s := SpecSummary{Spec: sp.String(), Fields: map[string]FieldTotal{}}
		var okCount int
		var sumMs int64
		for _, r := range runs {
			if r.Spec != s.Spec {
				continue
			}
			s.Images++
			s.Matched += r.Score.Matched
			s.Scored += r.Score.Scored
			for name, v := range r.Score.Fields {
				ft := s.Fields[name]
				if v != verdictSkip {
					ft.Scored++
				}
				if v == verdictMatch {
					ft.Matched++
				}
				s.Fields[name] = ft
			}
			if r.Error != "" {
				s.Errors++
				continue
			}
			okCount++
			sumMs += r.Millis
			if r.Millis > s.MaxMs {
				s.MaxMs = r.Millis
			}
		}
		if okCount > 0 {
			s.AvgMs = sumMs / int64(okCount)
		}
		if s.Scored > 0 {
			s.Accuracy = float64(s.Matched) / float64(s.Scored)
		}
		out = append(out, s)
	}
	return out
}

// NewReport assembles the report.
func NewReport(specs []Spec, runs []Run, now time.Time) Report {
	return Report{GeneratedAt: now.UTC().Format(time.RFC3339), Summary: Summarize(specs, runs), Runs: runs}
}

func pct(m, s int) string {
	if s == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", m, s)
}

// mdCell keeps a table cell on one line.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

// Markdown renders the summary, the per-field table and the per-image table.
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Poster reading benchmark\n\nGenerated %s.\n\n", r.GeneratedAt)

	b.WriteString("## Summary\n\n| Model | Images | Errors | Accuracy | Fields right | Avg ms | Max ms |\n|---|---|---|---|---|---|---|\n")
	for _, s := range r.Summary {
		fmt.Fprintf(&b, "| %s | %d | %d | %.1f%% | %s | %d | %d |\n",
			mdCell(s.Spec), s.Images, s.Errors, s.Accuracy*100, pct(s.Matched, s.Scored), s.AvgMs, s.MaxMs)
	}

	b.WriteString("\n## By field\n\n| Model |")
	for _, f := range fieldOrder {
		b.WriteString(" " + f + " |")
	}
	b.WriteString("\n|---|")
	for range fieldOrder {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, s := range r.Summary {
		fmt.Fprintf(&b, "| %s |", mdCell(s.Spec))
		for _, f := range fieldOrder {
			ft := s.Fields[f]
			fmt.Fprintf(&b, " %s |", pct(ft.Matched, ft.Scored))
		}
		b.WriteString("\n")
	}

	// Per image: files in first-seen order, one column per spec.
	var files []string
	seen := map[string]bool{}
	byKey := map[string]Run{}
	for _, run := range r.Runs {
		if !seen[run.File] {
			seen[run.File] = true
			files = append(files, run.File)
		}
		byKey[run.File+"\x00"+run.Spec] = run
	}
	b.WriteString("\n## By image\n\n| Image |")
	for _, s := range r.Summary {
		b.WriteString(" " + mdCell(s.Spec) + " |")
	}
	b.WriteString("\n|---|")
	for range r.Summary {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, f := range files {
		fmt.Fprintf(&b, "| %s |", mdCell(f))
		for _, s := range r.Summary {
			run, ok := byKey[f+"\x00"+s.Spec]
			switch {
			case !ok:
				b.WriteString(" - |")
			case run.Error != "":
				fmt.Fprintf(&b, " ERROR: %s |", mdCell(run.Error))
			default:
				fmt.Fprintf(&b, " %s (%.1fs) |", pct(run.Score.Matched, run.Score.Scored), float64(run.Millis)/1000)
			}
		}
		b.WriteString("\n")
	}

	// Misses, so the operator sees what went wrong without opening the JSON.
	var misses []string
	for _, run := range r.Runs {
		if run.Error != "" {
			continue
		}
		var names []string
		for name, v := range run.Score.Fields {
			if v == verdictMiss {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			misses = append(misses, fmt.Sprintf("- %s / %s: %s", mdCell(run.File), mdCell(run.Spec), strings.Join(names, ", ")))
		}
	}
	if len(misses) > 0 {
		b.WriteString("\n## Missed fields\n\n" + strings.Join(misses, "\n") + "\n")
	}
	return b.String()
}
