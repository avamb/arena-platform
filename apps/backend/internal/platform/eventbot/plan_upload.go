package eventbot

// plan_upload.go — the bot's half of "send the hall plan" (wizard_plan_upload.go
// holds the wizard's half).
//
// The person sends an SVG as a file. The bot checks it by its CONTENT (the
// file name proves nothing), reads it with the platform's own importer BEFORE
// anything is stored (so a bad file leaves no plan and no media object behind),
// then makes the plan, stores the file and imports the first version through the
// same API calls the admin console uses. What it found is told in plain words:
// the places, each category with its places, and what the importer warned
// about. A file it cannot use never moves the wizard.

import (
	"bytes"
	"context"
	"encoding/xml"
	"log/slog"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// planMaxBytes is the biggest SVG the bot takes. The version call carries the
// file inside a 4 MiB JSON body, so the file stays well under that.
const planMaxBytes = 3 << 20

// planNameMax is the longest plan name made from a file name.
const planNameMax = 80

// maxPlanIssues is how many problems of one file are listed; the rest are
// counted.
const maxPlanIssues = 5

// wizardPlanFile routes a file (or a photo) to a draft waiting for the hall
// plan. It reports whether the draft consumed the message.
func (b *Bot) wizardPlanFile(ctx context.Context, chatID int64, from *models.User, m *models.Message) bool {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil {
		return false
	}
	ws := b.wizardSession(id, jwt)
	d, _, err := b.loadDraft(ctx, from.ID, ws)
	if err != nil || d == nil || d.Paused || d.Step != stSPlanFile {
		return false
	}
	loc := ws.Locale
	again := func(key string, vars map[string]any) bool {
		// The wizard stays where it is: the same screen, with the reason above.
		b.wizardRender(ctx, chatID, nil, ws, d, b.texts.T(loc, key, vars))
		return true
	}
	if m.Document == nil {
		return again("bot.wz.plan_up_need_file", nil)
	}
	limit := map[string]any{"Limit": planMaxBytes >> 20}
	if m.Document.FileSize > planMaxBytes {
		return again("bot.wz.plan_up_too_big", limit)
	}
	data, err := b.downloadTelegramFile(ctx, m.Document.FileID)
	if err != nil {
		b.logger.Warn("eventbot: plan download failed", slog.String("error", err.Error()))
		return again("bot.wz.plan_up_failed", nil)
	}
	if len(data) > planMaxBytes {
		return again("bot.wz.plan_up_too_big", limit)
	}
	if !looksLikeSVG(data) {
		return again("bot.wz.plan_up_not_svg", nil)
	}

	venue := d.session()
	venueID, perr := uuid.Parse(venue.VenueID)
	if perr != nil {
		return again("bot.wz.plan_up_failed", nil)
	}

	// Read it first: nothing is stored for a file the importer refuses.
	geometry, warnings, issues := seating.ImportSVG(data)
	planType := planTypeOf(geometry)
	if len(issues) == 0 {
		issues = seating.ValidateForPlanType(geometry, planType)
	}
	if len(issues) > 0 {
		return again("bot.wz.plan_up_bad", map[string]any{"List": b.planIssuesText(loc, issues)})
	}

	name := planNameFor(m.Document.FileName, venue.VenueName)
	plan, err := b.arena.CreateSeatingPlan(ctx, jwt, ws.OrgID, venueID.String(), name, planType)
	if err != nil {
		b.logger.Warn("eventbot: plan create failed", slog.String("error", err.Error()))
		return again("bot.wz.plan_up_failed", nil)
	}
	// From here a failure takes the empty plan out of use again.
	fail := func(err error) bool {
		b.logger.Warn("eventbot: plan upload failed", slog.String("error", err.Error()))
		clean := context.WithoutCancel(ctx)
		if aerr := b.arena.ArchiveSeatingPlan(clean, jwt, plan.Id); aerr != nil {
			b.logger.Warn("eventbot: archiving the unfinished plan failed", slog.String("error", aerr.Error()))
		}
		return again("bot.wz.plan_up_failed", nil)
	}
	fileName := m.Document.FileName
	if !strings.EqualFold(path.Ext(fileName), ".svg") {
		fileName = name + ".svg"
	}
	up, err := b.arena.UploadOwnedMedia(ctx, jwt, ws.OrgID, seatingPlanSVGOwnerType, plan.Id.String(), fileName, "image/svg+xml", data)
	if err != nil {
		return fail(err)
	}
	res, err := b.arena.CreatePlanVersion(ctx, jwt, plan.Id, string(data), up.ID)
	if err != nil {
		return fail(err)
	}
	ref, ok := planRefOf(res.Plan, res.Version)
	if !ok {
		return fail(errNoPlacesStored)
	}
	warnCodes := res.Warnings
	if len(warnCodes) == 0 {
		for _, w := range warnings {
			warnCodes = append(warnCodes, w.Code)
		}
	}
	b.wizardApply(ctx, chatID, nil, from, ws, d, WizInput{Plan: &PlanUploaded{Plan: ref, Report: b.planReport(loc, ref, warnCodes)}})
	return true
}

type planUploadError string

func (e planUploadError) Error() string { return string(e) }

// errNoPlacesStored: the stored version has no category with places (the
// importer accepted a file the plan question could not use).
const errNoPlacesStored = planUploadError("stored hall plan has no category with places")

// looksLikeSVG reports whether the bytes are an XML document whose root
// element is <svg>. It looks at the content only: a file called plan.svg that
// is a picture, or a plan sent as plan.txt, is told apart correctly.
func looksLikeSVG(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 || !utf8.Valid(data[:min(len(data), 4096)]) {
		return false
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	for i := 0; i < 64; i++ {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == "svg"
		}
	}
	return false
}

// planTypeOf picks the plan type the geometry fits: the API refuses a plan
// whose type disagrees with its places.
func planTypeOf(g seating.Geometry) string {
	ga := 0
	for _, c := range g.Categories {
		if c.IsGA() {
			ga++
		}
	}
	switch {
	case ga > 0 && g.SeatCount() > 0:
		return "mixed"
	case ga > 0:
		return "general_admission"
	}
	return "assigned_seats"
}

// planNameFor names the plan after the file (without its extension), or after
// the venue when the file has no usable name.
func planNameFor(fileName, venueName string) string {
	base := strings.TrimSpace(strings.TrimSuffix(path.Base(strings.ReplaceAll(fileName, "\\", "/")), path.Ext(fileName)))
	base = strings.TrimSpace(strings.NewReplacer("_", " ").Replace(base))
	if base == "" || base == "." || base == "/" {
		base = strings.TrimSpace(venueName)
	}
	if base == "" {
		base = "Hall plan"
	}
	return truncate(base, planNameMax)
}

// planIssueKey maps an importer code to the sentence that explains it.
func planIssueKey(code string) string {
	switch code {
	case seating.ErrInvalidSVG:
		return "bot.wz.plan_up_issue_invalid"
	case seating.ErrCanvasMissing, seating.ErrCanvasTooLarge:
		return "bot.wz.plan_up_issue_canvas"
	case seating.ErrPriceCategoryMissing, seating.ErrPriceCategoryEmpty, seating.ErrPriceCategoryUnlabelled:
		return "bot.wz.plan_up_issue_categories"
	case seating.ErrSeatNotCircle:
		return "bot.wz.plan_up_issue_seat_shape"
	case seating.ErrRowMissingTitle, seating.ErrRowMissingSectorLabel, seating.ErrEmptyRow, seating.ErrEmptySection:
		return "bot.wz.plan_up_issue_row"
	case seating.ErrSeatMissingNumber, seating.ErrDuplicateSeat:
		return "bot.wz.plan_up_issue_seat_number"
	case seating.ErrSeatColorUnmatched, seating.ErrGAColorUnmatched:
		return "bot.wz.plan_up_issue_color"
	case seating.ErrTooManyCategories:
		return "bot.wz.plan_up_issue_too_many"
	case seating.ErrSeatsMissing, seating.ErrGAAreaMissing:
		return "bot.wz.plan_up_issue_no_places"
	case seating.ErrGACapacityInvalid, seating.ErrGAShapeUnsupported, seating.ErrGADuplicateCategory,
		seating.ErrGAAreaNotAllowed, seating.ErrSeatInGACategory, seating.ErrSeatsNotAllowed:
		return "bot.wz.plan_up_issue_ga"
	}
	return "bot.wz.plan_up_issue_other"
}

// planIssuesText lists what the importer refused, one line each, in words.
func (b *Bot) planIssuesText(loc string, issues seating.ValidationErrors) string {
	var lines []string
	seen := map[string]bool{}
	for _, is := range issues {
		key := planIssueKey(is.Code)
		element := "—"
		if e := truncate(is.Element, 60); e != "" {
			element = "«" + Esc(e) + "»"
		}
		line := b.texts.T(loc, key, map[string]any{"Element": element, "Code": Esc(is.Code)})
		if seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, "• "+line)
	}
	if len(lines) > maxPlanIssues {
		more := len(lines) - maxPlanIssues
		lines = append(lines[:maxPlanIssues], b.texts.T(loc, "bot.wz.plan_up_more_issues", map[string]any{"Count": more}))
	}
	return strings.Join(lines, "\n")
}

// planReport tells what was found in the plan: the places, each category with
// its places, and the importer's advice.
func (b *Bot) planReport(loc string, ref PlanRef, warnCodes []string) string {
	t := func(key string, vars map[string]any) string { return b.texts.T(loc, key, vars) }
	lines := []string{t("bot.wz.plan_up_done", map[string]any{"Name": Esc(ref.Name), "Seats": ref.Seats, "Count": len(ref.Categories)})}
	for _, c := range ref.Categories {
		key := "bot.wz.plan_up_line"
		if c.GA {
			key = "bot.wz.plan_up_line_ga"
		}
		lines = append(lines, t(key, map[string]any{"Name": Esc(c.Name), "Places": c.Places}))
	}
	if len(ref.Categories) == 1 {
		lines = append(lines, "", t("bot.wz.plan_up_one_category", nil))
	}
	for _, code := range warnCodes {
		if code == seating.WarnLegendMissing {
			lines = append(lines, "", t("bot.wz.plan_up_warn_legend", nil))
			continue
		}
		lines = append(lines, "", t("bot.wz.plan_up_warn_other", map[string]any{"Code": Esc(code)}))
	}
	return strings.Join(lines, "\n")
}
