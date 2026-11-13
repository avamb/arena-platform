package eventbot

// wizard_plan_upload.go — the seating plan question of a venue without a plan.
//
// A venue the organizer picked (or just made) may have no seating plan. The
// wizard offers two ways on: send the files of the plan (an administrator or,
// later, the generator builds it and the organizer checks it, spec
// 08_architecture/64), or go on without seats. The wizard stays a pure state
// machine: it asks and waits (stSPlanAsk, stSPlanFile). The bot
// (plan_intake.go) collects the files, posts them as ONE intake and, once the
// organizer approves the draft, hands the new plan back as WizInput.Plan; the
// date's plan question then follows with the new plan in the list. While the
// intake waits, the draft keeps PlanIntakeID and stays paused on stSPlanFile.

import (
	"context"
	"strconv"
)

// Steps of the plan upload.
const (
	stSPlanAsk  = "s_plan_ask"  // the venue has no plan: send files, or go on without seats
	stSPlanFile = "s_plan_file" // collecting the files, or waiting for the intake
)

// Where the wizard goes when the date is sold without a plan.
const (
	planAfterCapacity = ""       // an existing venue: the date's places are asked next
	planAfterFinish   = "finish" // a venue just made: its places were already asked
)

// PlanUploader tells the wizard that its reference source can take the files of
// a seating plan. It is optional: a source without it never offers the upload.
type PlanUploader interface {
	CanUploadPlans() bool
}

// PlanUploaded is a seating plan an approved intake made: the plan as the
// date's plan question lists it and the text the person is shown first.
type PlanUploaded struct {
	Plan   PlanRef
	Report string
}

func (w *Wizard) canUploadPlans() bool {
	u, ok := w.refs.(PlanUploader)
	return ok && u.CanUploadPlans()
}

// planUploadAsk moves the date to the "send files or go on" question.
func (d *Draft) planUploadAsk(after string) {
	d.Scratch.PlanAfter = after
	d.goTo(stSPlanAsk)
}

// withoutPlan ends the plan questions for a date sold without seats.
func (w *Wizard) withoutPlan(loc string, d *Draft) (string, error) {
	d.PlanIntakeID, d.PlanIntakeState = "", ""
	if d.Scratch.PlanAfter == planAfterFinish {
		return w.finishSession(loc, d)
	}
	if len(d.Scratch.Plans) == 0 {
		// An existing venue with no plan, and none sent: say so on the places
		// question and offer another venue, as when the plan is not offered.
		d.Scratch.NoPlanVenueID = d.session().VenueID
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
	if d.Step == stSPlanFile || (d.Step == stSPlan && in.Plan != nil) {
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
			d.PlanIntakeID, d.PlanIntakeState = "", ""
			d.IntakePlans = append(d.IntakePlans, in.Plan.Plan.VersionID)
			// "Back" from the plan question goes to the question before the
			// upload, not to a screen that waits for files already sent.
			if n := len(d.History); n > 0 && (d.History[n-1] == stSPlanAsk || d.History[n-1] == stSPlan) {
				d.History = d.History[:n-1]
			}
			d.Step = stSPlan
			return in.Plan.Report, nil
		}
		if in.Text != "" {
			return w.texts.T(loc, "bot.wz.plan_in_need_file", nil), nil
		}
	}
	return "", nil
}

// renderPlanUpload draws the two upload steps.
func (w *Wizard) renderPlanUpload(loc string, d *Draft, header func(int, string) string, nav func(...[]Button) [][]Button) Screen {
	t := func(key string, vars map[string]any) string { return w.texts.T(loc, key, vars) }
	skip := []Button{{Label: t("bot.wz.plan_in_skip_btn", nil), Data: "plan:skip"}}
	switch d.Step {
	case stSPlanAsk:
		s := d.session()
		return Screen{
			Text: header(2, "bot.wz.title_when") + t("bot.wz.plan_in_ask", map[string]any{"Venue": Esc(s.VenueName)}),
			Buttons: nav(
				[]Button{{Label: t("bot.wz.plan_in_send_btn", nil), Data: "plan:upload"}},
				skip,
			),
		}
	case stSPlanFile:
		if d.PlanIntakeID != "" {
			// The files are with an administrator: the screen a paused draft
			// comes back to. The plan can be checked once it is in review.
			buttons := [][]Button{}
			if d.PlanIntakeState == "review" {
				buttons = append(buttons, []Button{{Label: t("bot.wz.plan_in_check_btn", nil), Data: planDataPrefix + "rv:" + d.PlanIntakeID}})
			}
			buttons = append(buttons, skip)
			return Screen{
				Text: header(2, "bot.wz.title_when") + t(planWaitKey(d.PlanIntakeState), nil),
				Buttons: nav(
					buttons...,
				),
			}
		}
		return Screen{
			Text: header(2, "bot.wz.title_when") + t("bot.wz.plan_in_file", map[string]any{
				"Limit": strconv.Itoa(planIntakeFileMax >> 20), "Count": planIntakeMaxFiles,
			}),
			Buttons: nav(skip),
		}
	}
	return Screen{Text: d.Step, Buttons: nav()}
}
