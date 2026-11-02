package analytics

import (
	"sort"
	"time"
)

// FlowEv is a flow-related raw event. Who is "<id_kind>:<id_value>" of the
// event, so two people never share a flow run.
type FlowEv struct {
	At      time.Time
	Surface string
	Name    string
	Who     string
	Props   map[string]any
}

// FlowRow is one row of analytics.flow_daily.
type FlowRow struct {
	Day     time.Time // UTC midnight
	Surface string
	Flow    string
	Step    string
	Entered int
	// LeftHere counts the runs (event center) or exits (bot) that stopped at
	// this step.
	LeftHere int
	Errors   int
	// MedianSeconds is the median time spent on the step, nil when unknown.
	MedianSeconds *int
}

// Names of the flows the rollup synthesizes.
const (
	// FlowWizard is the Telegram bot's event wizard (wizard_* events carry no
	// flow name).
	FlowWizard = "wizard"
	// FlowScreens collects ui_error events that happened outside any flow run:
	// the step is the screen.
	FlowScreens = "_screens"
)

type flowKey struct {
	day                 time.Time
	surface, flow, step string
}

type flowAcc struct {
	entered, left, errors int
	seconds               []int
}

// ComputeFlowDaily builds the daily rows from flow_step, ui_error,
// wizard_step, wizard_input_error and wizard_exit events (spec 50 section 7).
//
// Event center: consecutive flow_step events of one user and flow with gaps
// of at most SessionGap form a run. Every flow_step enters its step; the
// seconds until the next step of the run are the time on the step; the last
// step of a run counts as left_here. A ui_error is booked on the step the
// user was on (the latest flow_step within SessionGap before it), or on the
// screen under FlowScreens when there is none.
//
// Bot: wizard_step enters its `to` step and carries the seconds spent on its
// `from` step; wizard_input_error books an error on its step; every
// wizard_exit except how=done books a left_here on its step.
func ComputeFlowDaily(evs []FlowEv) []FlowRow {
	sorted := append([]FlowEv(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	acc := map[flowKey]*flowAcc{}
	get := func(at time.Time, surface, flow, step string) *flowAcc {
		k := flowKey{TruncateDay(at), surface, flow, step}
		a := acc[k]
		if a == nil {
			a = &flowAcc{}
			acc[k] = a
		}
		return a
	}
	str := func(e FlowEv, key string) string { s, _ := e.Props[key].(string); return s }

	type cur struct {
		flow, step string
		at         time.Time
	}
	// last flow_step per user (any flow) for error attribution, and per
	// (user, flow) for run building.
	lastAny := map[string]cur{}
	lastByFlow := map[[2]string]cur{}

	for _, e := range sorted {
		switch e.Name {
		case "flow_step":
			flow, step := str(e, "flow"), str(e, "step")
			if flow == "" || step == "" {
				continue
			}
			get(e.At, e.Surface, flow, step).entered++
			key := [2]string{e.Who, flow}
			if prev, ok := lastByFlow[key]; ok && e.At.Sub(prev.at) <= SessionGap {
				if sec := int(e.At.Sub(prev.at).Seconds()); sec >= 0 {
					pa := get(prev.at, e.Surface, prev.flow, prev.step)
					pa.seconds = append(pa.seconds, sec)
				}
			} else if ok {
				// The previous run ended at prev.step.
				get(prev.at, e.Surface, prev.flow, prev.step).left++
			}
			c := cur{flow, step, e.At}
			lastByFlow[key] = c
			lastAny[e.Who] = c
		case "ui_error":
			if c, ok := lastAny[e.Who]; ok && e.At.Sub(c.at) <= SessionGap {
				get(e.At, e.Surface, c.flow, c.step).errors++
			} else if screen := str(e, "screen"); screen != "" {
				get(e.At, e.Surface, FlowScreens, screen).errors++
			}
		case "wizard_step":
			if to := str(e, "to"); to != "" {
				get(e.At, e.Surface, FlowWizard, to).entered++
			}
			if from := str(e, "from"); from != "" {
				if f, ok := e.Props["seconds"].(float64); ok && f >= 0 {
					a := get(e.At, e.Surface, FlowWizard, from)
					a.seconds = append(a.seconds, int(f))
				}
			}
		case "wizard_input_error":
			if step := str(e, "step"); step != "" {
				get(e.At, e.Surface, FlowWizard, step).errors++
			}
		case "wizard_exit":
			if step := str(e, "step"); step != "" && str(e, "how") != "done" {
				get(e.At, e.Surface, FlowWizard, step).left++
			}
		}
	}
	// The runs still open at the end of the input ended at their last step.
	for _, c := range lastByFlow {
		get(c.at, SurfaceEventCenter, c.flow, c.step).left++
	}

	out := make([]FlowRow, 0, len(acc))
	for k, a := range acc {
		if a.entered == 0 && a.left == 0 && a.errors == 0 {
			continue
		}
		row := FlowRow{Day: k.day, Surface: k.surface, Flow: k.flow, Step: k.step,
			Entered: a.entered, LeftHere: a.left, Errors: a.errors}
		if m, ok := medianInt(a.seconds); ok {
			row.MedianSeconds = &m
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case !a.Day.Equal(b.Day):
			return a.Day.Before(b.Day)
		case a.Surface != b.Surface:
			return a.Surface < b.Surface
		case a.Flow != b.Flow:
			return a.Flow < b.Flow
		}
		return a.Step < b.Step
	})
	return out
}

// medianInt is the median of the values (the lower middle of an even count
// is averaged and rounded down).
func medianInt(v []int) (int, bool) {
	if len(v) == 0 {
		return 0, false
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return (s[n/2-1] + s[n/2]) / 2, true
}
