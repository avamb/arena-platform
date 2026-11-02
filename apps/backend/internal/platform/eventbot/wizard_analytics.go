package eventbot

// wizard_analytics.go — what the event wizard tells product analytics (spec 50
// §4.4, §5): wizard_step on every move between steps, wizard_input_error with
// a reason CODE when an answer is refused, wizard_exit when the wizard ends.
// Only step names, mode and constant codes leave this file; the typed answer
// never does.

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// wizardReasonCodes names the refusal a wizard text stands for: the message key
// without "bot.wz." to the code analytics stores. A key missing here is stored
// as the key without its "err_" prefix (TestWizardReasonCodes_CoverEveryErrKey
// keeps the table complete).
var wizardReasonCodes = map[string]string{
	"err_name":                 "name_invalid",
	"err_poster_first":         "poster_required",
	"err_promoter_name":        "promoter_name_empty",
	"err_promoter_dup":         "promoter_duplicate",
	"err_date":                 "date_unparsed",
	"err_date_past":            "date_past",
	"err_date_early":           "date_too_early",
	"err_date_late":            "date_too_late",
	"err_time":                 "time_invalid",
	"err_sales_end":            "sales_end_invalid",
	"err_doors":                "doors_invalid",
	"err_duplicate_session":    "session_duplicate",
	"err_city_name":            "city_name_invalid",
	"err_venue_name":           "venue_name_invalid",
	"err_capacity":             "capacity_invalid",
	"err_venue_tz":             "timezone_invalid",
	"err_price":                "price_invalid",
	"err_change_date_order":    "change_date_order",
	"err_cat_dup":              "category_duplicate",
	"err_places":               "places_invalid",
	"err_cat_end":              "category_end_invalid",
	"err_until_order":          "category_end_order",
	"err_min_two":              "categories_min_two",
	"err_currency":             "currency_invalid",
	"err_channels_empty":       "channels_empty",
	"err_plan_extra_name":      "plan_extra_name_invalid",
	"err_plan_extra_dup":       "plan_extra_duplicate",
	"err_forbidden":            "forbidden",
	"err_description":          "description_invalid",
	"err_description_too_long": "description_too_long",
	"err_description_chars":    "description_chars",
}

// wizardReason is the stored code for a refusal message key.
func wizardReason(key string) string {
	key = strings.TrimPrefix(key, "bot.wz.")
	if code, ok := wizardReasonCodes[key]; ok {
		return code
	}
	return strings.TrimPrefix(key, "err_")
}

// reject remembers (for analytics, in this Apply call only) that the wizard
// refused the answer with the message key, and returns the note unchanged.
func (d *Draft) reject(key, note string) string {
	d.rejected = key
	return note
}

// maxStepSeconds is the longest step time the dictionary stores (one day).
const maxStepSeconds = 86400

// wizardTurn is the state of a draft before one answer is applied.
type wizardTurn struct {
	step  string // the step the answer was given on
	since int64  // when the draft reached it, Unix seconds; 0 = unknown
	mode  string
	// cancelFrom is the step a cancel screen was opened from, "" otherwise.
	cancelFrom string
}

func newWizardTurn(d *Draft) wizardTurn {
	t := wizardTurn{step: d.Step, since: d.StepSince, mode: d.Mode}
	if d.Step == stCancel {
		t.cancelFrom = d.Scratch.CancelFrom
	}
	return t
}

// exitStep is the step a person was on when they left: the step under the
// cancel screen, else the step itself.
func (t wizardTurn) exitStep() string {
	if t.cancelFrom != "" {
		return t.cancelFrom
	}
	return t.step
}

// trackWizard records what one Apply did and stamps the draft with the time it
// reached its new step. Called by wizardApply right after Apply, before the
// draft is stored. orgID is the organization the draft belongs to.
func (b *Bot) trackWizard(ctx context.Context, tg int64, orgID uuid.UUID, turn wizardTurn, d *Draft, err error) {
	now := b.clock()
	switch {
	case errors.Is(err, ErrCancelled):
		b.an.track(ctx, tg, &orgID, "wizard_exit", map[string]any{"step": turn.exitStep(), "how": "drop"})
		return
	case errors.Is(err, ErrSavedExit):
		b.an.track(ctx, tg, &orgID, "wizard_exit", map[string]any{"step": turn.exitStep(), "how": "keep"})
		return
	case err != nil:
		return
	}
	if d.rejected != "" {
		b.an.track(ctx, tg, &orgID, "wizard_input_error", map[string]any{"step": turn.step, "reason": wizardReason(d.rejected)})
		d.rejected = ""
	}
	if d.Step == turn.step {
		return
	}
	props := map[string]any{"from": turn.step, "to": d.Step, "mode": turn.mode}
	if turn.since > 0 {
		if secs := now.Unix() - turn.since; secs >= 0 {
			// The dictionary drops more than a day, so a draft left overnight
			// still reports its step time.
			props["seconds"] = min(secs, maxStepSeconds)
		}
	}
	d.StepSince = now.Unix()
	b.an.track(ctx, tg, &orgID, "wizard_step", props)
	// Opening the cancel screen is not leaving: a person who presses
	// "continue" stays. The exits are keep, drop, done and expired only.
}

// trackPosterRefusal records a poster the bot refused before the wizard saw it
// (too big, wrong type, a PDF, wrong ratio, too small). reason is the message
// key without "bot.wz." ("poster_too_small").
func (b *Bot) trackPosterRefusal(ctx context.Context, tg int64, orgID uuid.UUID, d *Draft, key string) {
	b.an.track(ctx, tg, &orgID, "wizard_input_error", map[string]any{"step": d.Step, "reason": strings.TrimPrefix(key, "bot.wz.")})
}
