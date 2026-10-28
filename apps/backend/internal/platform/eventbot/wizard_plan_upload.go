package eventbot

// wizard_plan_upload.go — a hall plan sent from the wizard.
//
// A venue the organizer picked (or just made) may have no seating plan, and the
// bot used to skip the plan question for it: the plan could only be loaded by an
// operator in the console. Now the wizard offers two ways on: send the hall as
// an SVG file, or go on without seats. The wizard stays a pure state machine:
// it asks and waits (stSPlanAsk, stSPlanFile); the bot (plan_upload.go)
// downloads the file, reads it, stores the plan through the API and hands the
// result back as WizInput.Plan, after which the date's plan question follows
// with the new plan in the list. A file the bot cannot use never moves the
// wizard: the person sends another file or goes on without a plan.

import (
	"context"
	"strconv"
)

// Steps of the plan upload.
const (
	stSPlanAsk  = "s_plan_ask"  // the venue has no plan: send one, or go on without seats
	stSPlanFile = "s_plan_file" // waiting for the SVG file
)

// Where the wizard goes when the date is sold without a plan.
const (
	planAfterCapacity = ""       // an existing venue: the date's places are asked next
	planAfterFinish   = "finish" // a venue just made: its places were already asked
)

// PlanUploader tells the wizard that its reference source can store a new hall
// plan. It is optional: a source without it never offers the upload.
type PlanUploader interface {
	CanUploadPlans() bool
}

// PlanUploaded is a hall plan the bot stored: the plan as the date's plan
// question lists it and the report the person is shown first.
type PlanUploaded struct {
	Plan   PlanRef
	Report string
}

func (w *Wizard) canUploadPlans() bool {
	u, ok := w.refs.(PlanUploader)
	return ok && u.CanUploadPlans()
}

// planUploadAsk moves the date to the "send a plan or go on" question.
func (d *Draft) planUploadAsk(after string) {
	d.Scratch.PlanAfter = after
	d.goTo(stSPlanAsk)
}

// withoutPlan ends the plan questions for a date sold without seats.
func (w *Wizard) withoutPlan(loc string, d *Draft) (string, error) {
	if d.Scratch.PlanAfter == planAfterFinish {
		return w.finishSession(loc, d)
	}
	d.goTo(stSCapacity)
	return "", nil
}

// applyPlanUpload handles the two upload steps.
func (w *Wizard) applyPlanUpload(ctx context.Context, ws WizSession, d *Draft, in WizInput, data string) (string, error) {
	loc := ws.Locale
	switch data {
	case "plan:upload":
		if d.Step == stSPlanAsk || d.Step == stSPlan {
			d.goTo(stSPlanFile)
		}
		return "", nil
	case "plan:skip":
		return w.withoutPlan(loc, d)
	}
	if d.Step == stSPlanFile {
		if in.Plan != nil {
			// The new plan leads the list; the venue's other plans follow.
			plans := []PlanRef{in.Plan.Plan}
			for _, p := range w.venuePlans(ctx, ws, d.session().VenueID) {
				if p.VersionID != in.Plan.Plan.VersionID {
					plans = append(plans, p)
				}
			}
			d.Scratch.Plans = plans
			d.Scratch.CanUpload = true
			// "Back" from the plan question goes to the question before the
			// upload, not to a screen that waits for a file already sent.
			if n := len(d.History); n > 0 && (d.History[n-1] == stSPlanAsk || d.History[n-1] == stSPlan) {
				d.History = d.History[:n-1]
			}
			d.Step = stSPlan
			return in.Plan.Report, nil
		}
		if in.Text != "" {
			return w.texts.T(loc, "bot.wz.plan_up_need_file", nil), nil
		}
	}
	return "", nil
}

// renderPlanUpload draws the two upload steps.
func (w *Wizard) renderPlanUpload(loc string, d *Draft, header func(int, string) string, nav func(...[]Button) [][]Button) Screen {
	t := func(key string, vars map[string]any) string { return w.texts.T(loc, key, vars) }
	skip := []Button{{Label: t("bot.wz.plan_up_skip_btn", nil), Data: "plan:skip"}}
	switch d.Step {
	case stSPlanAsk:
		s := d.session()
		return Screen{
			Text: header(2, "bot.wz.title_when") + t("bot.wz.plan_up_ask", map[string]any{"Venue": Esc(s.VenueName)}),
			Buttons: nav(
				[]Button{{Label: t("bot.wz.plan_up_btn", nil), Data: "plan:upload"}},
				skip,
			),
		}
	case stSPlanFile:
		return Screen{
			Text:    header(2, "bot.wz.title_when") + t("bot.wz.plan_up_file", map[string]any{"Limit": strconv.Itoa(planMaxBytes >> 20)}),
			Buttons: nav(skip),
		}
	}
	return Screen{Text: d.Step, Buttons: nav()}
}
