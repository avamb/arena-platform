package eventbot

// plan_intake.go — the bot's half of the seating plan intake (spec
// 08_architecture/64, wave 1: an administrator builds the draft).
//
// The organizer sends the files of a plan (documents and photos, up to 10 files,
// 20 MB each, 80 MB together) and presses "Done". The bot posts them as ONE
// intake of the venue. A ready SVG becomes draft 1 on the server and the intake
// starts in "review"; anything else goes to a platform administrator, who
// builds a draft and returns it. The organizer then checks the draft on a
// picture (planpreview), and either approves it, asks for a fix in words, or
// calls an administrator. An approved intake is a seating plan of the venue.
//
// Presses are "pi:<kind>:<arg>":
//
//	pi:up:<venue>          start collecting files for a venue (no draft touched)
//	pi:done                post the collected files        pi:drop    forget them
//	pi:rv:<intake>         the review screen (or the state of the intake)
//	pi:ok:<intake>:<n>     approve draft n                 pi:fix:<intake>:<n>  ask for a fix
//	pi:adm:<intake>        call an administrator           pi:cx:<intake>   cancel the intake
//	pi:again:<intake>      send other files                pi:skip:<intake> go on without a plan
//	pi:rm:<n|last>         remove a collected file         pi:sky|skn:<intake>  cancel the request or keep it
//	pi:ro:<org>:<intake>   the review button of a notice of another organization
//
// The collected files live in ONE bot_dialogs row of kind "planintake" (file
// ids only, nothing is downloaded before "Done"). A draft of the "+ Event"
// wizard that waits for an intake is paused on its plan step and keeps the
// intake id (Draft.PlanIntakeID); when the plan is approved the wizard goes on
// with the new plan at the head of the plan list.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/planpreview"
)

const (
	planIntakeDialogKind = "planintake"
	// planDataPrefix starts the callback data of this feature.
	planDataPrefix = "pi:"

	piStepCollect = "collect"
	piStepFix     = "fix"
	// piStepSent marks a dialog whose files went out as an intake.
	piStepSent = "sent"

	// The limits of one intake: the server's, which are Telegram's too.
	planIntakeMaxFiles = 10
	planIntakeFileMax  = 20 << 20
	planIntakeTotalMax = 80 << 20
	// planFixMaxRunes is the longest text of a correction the server takes.
	planFixMaxRunes = 2000
	// planReviewRoundsHint is the round after which an administrator is offered.
	planReviewRoundsHint = 5
	// planPreviewSide is the longest side of the preview picture.
	planPreviewSide = 1280
)

// planIntakeFileRef is a file the person sent: its Telegram id, never its bytes.
type planIntakeFileRef struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Photo  bool   `json:"photo,omitempty"`
}

// planIntakeDialog is the state of the "planintake" dialog.
type planIntakeDialog struct {
	OrgID   uuid.UUID `json:"org_id"`
	VenueID string    `json:"venue_id,omitempty"`
	// Wizard: the files belong to the wizard's plan step (the draft is paused
	// on the intake once they are posted).
	Wizard bool `json:"wizard,omitempty"`
	// Key is the Idempotency-Key of the post: a repeated "Done" sends nothing
	// twice.
	Key      string              `json:"key,omitempty"`
	Files    []planIntakeFileRef `json:"files,omitempty"`
	PhotoTip bool                `json:"photo_tip,omitempty"`
	// IntakeID and Draft name the draft a typed correction is about (step fix).
	IntakeID string `json:"intake_id,omitempty"`
	Draft    int    `json:"draft,omitempty"`
}

func (b *Bot) leavePlanIntake(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, planIntakeDialogKind); err != nil {
		b.logger.Warn("eventbot: plan intake dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

func (b *Bot) savePlanIntake(ctx context.Context, tg int64, st planIntakeDialog, step string) error {
	org := st.OrgID
	return b.dialogs.Save(ctx, tg, &org, planIntakeDialogKind, step, st, dialogTTL)
}

// planFileOf reads the file of a message: a document as it is, a photo as its
// largest size.
func planFileOf(m *models.Message, n int) (planIntakeFileRef, bool) {
	switch {
	case m.Document != nil:
		name := strings.TrimSpace(m.Document.FileName)
		if name == "" {
			name = "file-" + strconv.Itoa(n)
		}
		return planIntakeFileRef{FileID: m.Document.FileID, Name: name, Size: m.Document.FileSize}, true
	case len(m.Photo) > 0:
		largest := m.Photo[0]
		for _, p := range m.Photo[1:] {
			if p.Width > largest.Width {
				largest = p
			}
		}
		return planIntakeFileRef{FileID: largest.FileID, Name: "photo-" + strconv.Itoa(n) + ".jpg", Size: int64(largest.FileSize), Photo: true}, true
	}
	return planIntakeFileRef{}, false
}

func piBtn(text string, parts ...string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: planDataPrefix + strings.Join(parts, ":")}
}

// ─── collecting the files ────────────────────────────────────────────────────

// planIntakeFile takes a document or photo while the person is sending a plan:
// either a "planintake" dialog is open, or the wizard waits on its plan step.
// It reports whether the message was taken.
func (b *Bot) planIntakeFile(ctx context.Context, chatID int64, from *models.User, m *models.Message) bool {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil {
		return false
	}
	var st planIntakeDialog
	step, found, _, err := b.dialogs.Load(ctx, from.ID, planIntakeDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: plan intake dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	loc := id.Locale()
	if !found || step != piStepCollect || st.OrgID != id.Current.OrgID {
		ws := b.wizardSession(id, jwt)
		d, _, derr := b.loadDraft(ctx, from.ID, ws)
		if derr != nil || d == nil || d.Step != stSPlanFile {
			return false
		}
		if d.PlanIntakeID != "" {
			// The draft waits for an intake: read what became of it before the
			// file is refused (a final intake is forgotten and the file is taken).
			if _, ok := planFileOf(m, 1); !ok {
				return false
			}
			if b.syncDraftIntake(ctx, chatID, ws, d) {
				b.send(ctx, chatID, b.texts.T(loc, "bot.wz.plan_in_already", nil), nil)
				return true
			}
		} else if d.Paused {
			return false // the menu paused the draft: a file is not an answer
		}
		st = planIntakeDialog{OrgID: ws.OrgID, VenueID: d.session().VenueID, Wizard: true, Key: uuid.NewString()}
	}
	ref, ok := planFileOf(m, len(st.Files)+1)
	if !ok {
		return false
	}
	if key, vars := planCheckAdd(st.Files, ref); key != "" {
		b.send(ctx, chatID, b.texts.T(loc, key, vars), b.planCollectKeyboard(loc, st))
		return true
	}
	st.Files = append(st.Files, ref)
	text := b.texts.T(loc, "bot.wz.plan_in_got", map[string]any{"Count": len(st.Files)})
	if ref.Photo && !st.PhotoTip {
		st.PhotoTip = true
		text += "\n\n" + b.texts.T(loc, "bot.wz.plan_in_photo_tip", nil)
	}
	// The files are stored before the person reads that they arrived.
	if err := b.savePlanIntake(ctx, from.ID, st, piStepCollect); err != nil {
		b.logger.Error("eventbot: plan intake dialog save failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return true
	}
	b.send(ctx, chatID, text, b.planCollectKeyboard(loc, st))
	return true
}

// planCollectKeyboard is under every message of the collection: "Done" once a
// file is there, and the way out.
func (b *Bot) planCollectKeyboard(loc string, st planIntakeDialog) *models.InlineKeyboardMarkup {
	return b.planCollectKeyboardFor(loc, st, "")
}

// planCheckAdd checks one more file against the limits from the sizes Telegram
// declares, before anything is downloaded. It returns the text key of the
// refusal and its variables, or "" when the file may join the list.
func planCheckAdd(files []planIntakeFileRef, add planIntakeFileRef) (string, map[string]any) {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	switch {
	case len(files) >= planIntakeMaxFiles:
		return "bot.wz.plan_in_too_many", map[string]any{"Count": planIntakeMaxFiles}
	case add.Size > planIntakeFileMax:
		return "bot.wz.plan_in_too_big", map[string]any{"Limit": planIntakeFileMax >> 20}
	case total+add.Size > planIntakeTotalMax:
		return "bot.wz.plan_in_too_big_total", map[string]any{"Limit": planIntakeTotalMax >> 20}
	}
	return "", nil
}

// planCheckList checks a whole collection the same way, for the moment before
// the download.
func planCheckList(files []planIntakeFileRef) (string, map[string]any) {
	var total int64
	for i, f := range files {
		switch {
		case i >= planIntakeMaxFiles:
			return "bot.wz.plan_in_too_many", map[string]any{"Count": planIntakeMaxFiles}
		case f.Size > planIntakeFileMax:
			return "bot.wz.plan_in_too_big", map[string]any{"Limit": planIntakeFileMax >> 20}
		}
		total += f.Size
		if total > planIntakeTotalMax {
			return "bot.wz.plan_in_too_big_total", map[string]any{"Limit": planIntakeTotalMax >> 20}
		}
	}
	return "", nil
}

// planPostKey is the Idempotency-Key of a post: the key of the dialog and the
// list of file ids. A retry of the same list is the same request, and any
// change of the list is a new intake.
func planPostKey(st planIntakeDialog) string {
	space := uuid.NewSHA1(uuid.NameSpaceURL, []byte(st.Key))
	ids := make([]string, 0, len(st.Files))
	for _, f := range st.Files {
		ids = append(ids, f.FileID)
	}
	return uuid.NewSHA1(space, []byte(strings.Join(ids, "\n"))).String()
}

// planPostRefusal maps the answer of the post to the text of a refusal of the
// files and the name of the file the server named (empty when it did not).
func planPostRefusal(err error) (key, name string, ok bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return "", "", false
	}
	switch {
	case ae.Code == "venue_plan_intake.unsupported_format", ae.Code == "venue_plan_intake.empty_file", ae.Status == http.StatusUnsupportedMediaType:
		return "bot.wz.plan_in_bad_format", ae.FileName, true
	case ae.Status == http.StatusRequestEntityTooLarge:
		return "bot.wz.plan_in_too_big_server", ae.FileName, true
	case ae.Status == http.StatusUnprocessableEntity:
		return "bot.wz.plan_in_unreadable", ae.FileName, true
	}
	return "", "", false
}

// planCollectKeyboardFor is planCollectKeyboard with a refused file: when the
// server named it, the person can remove exactly that file.
func (b *Bot) planCollectKeyboardFor(loc string, st planIntakeDialog, refused string) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	if len(st.Files) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_done_btn", nil), "done")})
		rm := piBtn(b.texts.T(loc, "bot.wz.plan_in_remove_last_btn", nil), "rm", "last")
		if refused != "" {
			for i, f := range st.Files {
				if f.Name == refused || planPartFileName(f.Name) == refused {
					rm = piBtn(truncate(b.texts.T(loc, "bot.wz.plan_in_remove_btn", map[string]any{"Name": f.Name}), 56), "rm", strconv.Itoa(i+1))
					break
				}
			}
		}
		rows = append(rows, []models.InlineKeyboardButton{rm})
	}
	if st.Wizard {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.wz.plan_in_skip_btn", nil), CallbackData: "wz:plan:skip"}})
	} else {
		rows = append(rows, []models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_discard_btn", nil), "drop")})
	}
	return inline(rows...)
}

// planIntakeText answers a typed text while the dialog is open: a correction
// (step fix) is posted, a text in the collection is not a file.
func (b *Bot) planIntakeText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st planIntakeDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, planIntakeDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: plan intake dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		if step != piStepFix {
			return false
		}
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), b.backKeyboard(loc, "home"))
		return true
	}
	if !found {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID {
		b.leavePlanIntake(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	switch step {
	case piStepCollect:
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.plan_in_need_file", nil), b.planCollectKeyboard(loc, st))
		return true
	case piStepFix:
		text = strings.TrimSpace(text)
		if text == "" {
			return false
		}
		if len([]rune(text)) > planFixMaxRunes {
			b.send(ctx, chatID, b.texts.T(loc, "bot.wz.plan_in_fix_long", map[string]any{"Limit": planFixMaxRunes}), nil)
			return true
		}
		_, err := b.arena.CorrectPlanIntakeText(ctx, jwt, st.OrgID, st.IntakeID, st.Draft, text)
		switch {
		case err == nil:
			b.leavePlanIntake(ctx, from.ID)
			b.send(ctx, chatID, b.texts.T(loc, "bot.wz.plan_in_fix_sent", nil), b.backKeyboard(loc, "home"))
		case IsAPIError(err, http.StatusConflict):
			b.leavePlanIntake(ctx, from.ID)
			b.planReview(ctx, chatID, nil, from, id, jwt, st.IntakeID, b.texts.T(loc, "bot.wz.plan_in_changed", nil)+"\n\n")
		default:
			b.logger.Warn("eventbot: plan correction failed", slog.String("error", err.Error()))
			b.send(ctx, chatID, b.texts.T(loc, "bot.wz.plan_in_fix_failed", nil), nil)
		}
		return true
	}
	return false
}

// ─── presses ─────────────────────────────────────────────────────────────────

// planIntakeCallback handles every "pi:<data>" press.
func (b *Bot) planIntakeCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	kind, arg, _ := strings.Cut(data, ":")
	switch kind {
	case "up":
		b.planStartCollect(ctx, chatID, &msgID, from, id, jwt, arg, false)
	case "done":
		b.planDone(ctx, chatID, &msgID, from, id, jwt)
	case "drop":
		b.leavePlanIntake(ctx, from.ID)
		b.showHome(ctx, chatID, &msgID, from, "")
	case "rv":
		b.planReview(ctx, chatID, &msgID, from, id, jwt, arg, "")
	case "ok":
		intake, n, good := splitIntakeDraft(arg)
		if !good {
			b.planReview(ctx, chatID, &msgID, from, id, jwt, intake, "")
			return
		}
		b.planApprove(ctx, chatID, &msgID, from, id, jwt, intake, n)
	case "fix":
		intake, n, good := splitIntakeDraft(arg)
		if !good {
			b.planReview(ctx, chatID, &msgID, from, id, jwt, intake, "")
			return
		}
		st := planIntakeDialog{OrgID: id.Current.OrgID, IntakeID: intake, Draft: n}
		if err := b.savePlanIntake(ctx, from.ID, st, piStepFix); err != nil {
			b.logger.Error("eventbot: plan intake dialog save failed", slog.String("error", err.Error()))
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
			return
		}
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.wz.plan_in_fix_ask", nil), inline(
			[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.btn_back", nil), "rv", intake)}))
	case "adm":
		b.planEscalate(ctx, chatID, &msgID, from, id, jwt, arg)
	case "cx":
		if err := b.arena.CancelPlanIntake(ctx, jwt, id.Current.OrgID, arg); err != nil {
			if IsAPIError(err, http.StatusConflict) {
				// The intake is no longer open (wrong_state): say what it is now.
				b.planReview(ctx, chatID, &msgID, from, id, jwt, arg, "")
				return
			}
			b.ecError(ctx, chatID, &msgID, id, err, "home")
			return
		}
		b.planForgetIntake(ctx, from.ID, id, jwt, arg)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.wz.plan_in_cancelled", nil), b.backKeyboard(loc, "home"))
	case "rm":
		b.planRemoveFile(ctx, chatID, &msgID, from, id, arg)
	case "ro":
		b.planOtherOrg(ctx, chatID, &msgID, from, id, jwt, arg)
	case "sky":
		b.planSkipCancel(ctx, chatID, &msgID, from, id, jwt, arg)
	case "skn":
		b.planSkip(ctx, chatID, &msgID, from, id, jwt, arg)
	case "again":
		b.planAgain(ctx, chatID, &msgID, from, id, jwt, arg)
	case "skip":
		b.planSkip(ctx, chatID, &msgID, from, id, jwt, arg)
	default:
		b.showHome(ctx, chatID, &msgID, from, "")
	}
}

func splitIntakeDraft(arg string) (intake string, n int, ok bool) {
	intake, raw, _ := strings.Cut(arg, ":")
	n, err := strconv.Atoi(raw)
	return intake, n, err == nil && n > 0
}

// planStartCollect starts the collection for a venue. An intake of the venue
// that is still open is shown instead of a second one.
func (b *Bot) planStartCollect(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, venueID string, wizard bool) {
	loc := id.Locale()
	if _, err := uuid.Parse(venueID); err != nil {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.ec.not_found", nil), b.backKeyboard(loc, "home"))
		return
	}
	if !wizard {
		items, err := b.arena.ListPlanIntakes(ctx, jwt, id.Current.OrgID, venueID, "")
		if err != nil {
			b.ecError(ctx, chatID, msgID, id, err, "home")
			return
		}
		for _, it := range items {
			if openPlanIntakeState(it.State) {
				b.planShowOpen(ctx, chatID, msgID, id, it)
				return
			}
		}
	}
	st := planIntakeDialog{OrgID: id.Current.OrgID, VenueID: venueID, Wizard: wizard, Key: uuid.NewString()}
	// The first file creates the dialog; this one only marks that the person
	// asked, so a press that arrives twice sends nothing twice.
	text := b.texts.T(loc, "bot.wz.plan_in_file", map[string]any{"Limit": planIntakeFileMax >> 20, "Count": planIntakeMaxFiles})
	if err := b.savePlanIntake(ctx, from.ID, st, piStepCollect); err != nil {
		b.logger.Error("eventbot: plan intake dialog save failed", slog.String("error", err.Error()))
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	b.reply(ctx, chatID, msgID, text, b.planCollectKeyboard(loc, st))
}

func openPlanIntakeState(state string) bool {
	switch state {
	case "received", "generating", "review", "operator":
		return true
	}
	return false
}

// planShowOpen says in words where an open intake of the venue stands.
func (b *Bot) planShowOpen(ctx context.Context, chatID int64, msgID *int, id *Identity, it openapi.VenuePlanIntakeListItem) {
	loc := id.Locale()
	vars := map[string]any{"Venue": Esc(it.VenueName)}
	var rows [][]models.InlineKeyboardButton
	key := "bot.wz.plan_in_open_wait"
	if it.State == "review" {
		key = "bot.wz.plan_in_open_review"
		rows = append(rows, []models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_check_btn", nil), "rv", it.Id.String())})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_cancel_btn", nil), "cx", it.Id.String())},
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
	b.reply(ctx, chatID, msgID, b.texts.T(loc, key, vars), inline(rows...))
}

// ─── "Done": the post ────────────────────────────────────────────────────────

func (b *Bot) planDone(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt string) {
	loc := id.Locale()
	var st planIntakeDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, planIntakeDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: plan intake dialog load failed", slog.String("error", err.Error()))
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	if expired {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.dialog_expired", nil), b.backKeyboard(loc, "home"))
		return
	}
	if found && step == piStepSent && st.OrgID == id.Current.OrgID {
		// A second press after the files went out: nothing is sent twice.
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_already_sent", nil), b.backKeyboard(loc, "home"))
		return
	}
	if !found || step != piStepCollect || st.OrgID != id.Current.OrgID || len(st.Files) == 0 {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_nothing", nil), b.backKeyboard(loc, "home"))
		return
	}
	// The limits are checked from the sizes Telegram declares, before any download.
	if key, vars := planCheckList(st.Files); key != "" {
		b.planRefuse(ctx, chatID, msgID, loc, st, key, vars, "")
		return
	}
	files := make([]PlanIntakeFile, 0, len(st.Files))
	var total int64
	for _, f := range st.Files {
		data, derr := b.downloadTelegramFileMax(ctx, f.FileID, planIntakeFileMax)
		if derr != nil {
			b.logger.Warn("eventbot: plan file download failed", slog.String("error", derr.Error()))
			b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_failed", nil), b.planCollectKeyboard(loc, st))
			return
		}
		total += int64(len(data))
		if len(data) > planIntakeFileMax || total > planIntakeTotalMax {
			b.planRefuse(ctx, chatID, msgID, loc, st, "bot.wz.plan_in_too_big_server", nil, f.Name)
			return
		}
		files = append(files, PlanIntakeFile{Name: f.Name, Data: data})
	}
	in, err := b.arena.CreatePlanIntake(ctx, jwt, st.OrgID, st.VenueID, planPostKey(st), files)
	if err != nil {
		if key, name, ok := planPostRefusal(err); ok {
			// The files stay in the dialog: the person removes one or sends others.
			b.planRefuse(ctx, chatID, msgID, loc, st, key, nil, name)
			return
		}
		if IsAPIError(err, http.StatusForbidden) {
			b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.err_forbidden", nil), b.backKeyboard(loc, "home"))
			return
		}
		b.logger.Warn("eventbot: plan intake post failed", slog.String("error", err.Error()))
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_failed", nil), b.planCollectKeyboard(loc, st))
		return
	}
	// The dialog stays as "sent", so a second "Done" is told the files are out.
	sent := planIntakeDialog{OrgID: st.OrgID, VenueID: st.VenueID, Wizard: st.Wizard, IntakeID: in.Id.String()}
	if err := b.savePlanIntake(ctx, from.ID, sent, piStepSent); err != nil {
		b.logger.Warn("eventbot: plan intake dialog not marked sent", slog.String("error", err.Error()))
		b.leavePlanIntake(ctx, from.ID)
	}
	if st.Wizard {
		ws := b.wizardSession(id, jwt)
		if d, _, derr := b.loadDraft(ctx, from.ID, ws); derr == nil && d != nil && d.Step == stSPlanFile {
			d.PlanIntakeID, d.PlanIntakeState = in.Id.String(), in.State
			d.Paused = true
			if _, serr := b.storeDraft(ctx, from.ID, ws, d); serr != nil {
				b.logger.Error("eventbot: wizard draft not paused on the intake", slog.String("error", serr.Error()))
			}
		}
	}
	if in.State == "review" {
		b.planReview(ctx, chatID, msgID, from, id, jwt, in.Id.String(), "")
		return
	}
	text := b.texts.T(loc, "bot.wz.plan_in_received", nil)
	for _, s := range in.Sources {
		if s.Kind == "svg" {
			text += "\n\n" + b.texts.T(loc, "bot.wz.plan_in_svg_note", nil)
			break
		}
	}
	if st.Wizard {
		text += "\n\n" + b.texts.T(loc, "bot.wz.plan_in_draft_saved", nil)
	}
	b.reply(ctx, chatID, msgID, text, b.backKeyboard(loc, "home"))
}

// planRefuse tells why the files were refused. The collected files stay: the
// person removes the one that does not fit (the server's name for it is given
// when there is one) or sends others. The dialog is not changed.
func (b *Bot) planRefuse(ctx context.Context, chatID int64, msgID *int, loc string, st planIntakeDialog, key string, vars map[string]any, name string) {
	if vars == nil {
		vars = map[string]any{}
	}
	vars["Name"] = Esc(name)
	b.reply(ctx, chatID, msgID, b.texts.T(loc, key, vars), b.planCollectKeyboardFor(loc, st, name))
}

// planRemoveFile takes one file out of the collection: "last", or the number of
// the file (from 1).
func (b *Bot) planRemoveFile(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, arg string) {
	loc := id.Locale()
	var st planIntakeDialog
	step, found, _, err := b.dialogs.Load(ctx, from.ID, planIntakeDialogKind, &st)
	if err != nil || !found || step != piStepCollect || st.OrgID != id.Current.OrgID || len(st.Files) == 0 {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_nothing", nil), b.backKeyboard(loc, "home"))
		return
	}
	i := len(st.Files) - 1
	if n, perr := strconv.Atoi(arg); perr == nil && n >= 1 && n <= len(st.Files) {
		i = n - 1
	}
	st.Files = append(st.Files[:i:i], st.Files[i+1:]...)
	if err := b.savePlanIntake(ctx, from.ID, st, piStepCollect); err != nil {
		b.logger.Error("eventbot: plan intake dialog save failed", slog.String("error", err.Error()))
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	if len(st.Files) == 0 {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_file", map[string]any{"Limit": planIntakeFileMax >> 20, "Count": planIntakeMaxFiles}), b.planCollectKeyboard(loc, st))
		return
	}
	b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_removed", map[string]any{"Count": len(st.Files)}), b.planCollectKeyboard(loc, st))
}

// ─── the review ──────────────────────────────────────────────────────────────

// planReview shows the state of an intake: in "review" the draft on a picture
// with the three buttons, otherwise what the person waits for.
func (b *Bot) planReview(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, intakeID, prefix string) {
	loc := id.Locale()
	in, err := b.arena.GetPlanIntake(ctx, jwt, id.Current.OrgID, intakeID)
	if err != nil {
		if IsAPIError(err, http.StatusNotFound) || IsAPIError(err, http.StatusBadRequest) {
			b.reply(ctx, chatID, msgID, prefix+b.texts.T(loc, "bot.wz.plan_in_gone", nil), b.backKeyboard(loc, "home"))
			return
		}
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	venue := map[string]any{"Venue": Esc(in.VenueName)}
	switch in.State {
	case "approved":
		b.planApproved(ctx, chatID, msgID, from, id, jwt, in)
		return
	case "rejected":
		b.planRejectedScreen(ctx, chatID, msgID, from, id, jwt, in.VenueName, in.Id.String(), in.RejectReason)
		return
	case "cancelled":
		b.reply(ctx, chatID, msgID, prefix+b.texts.T(loc, "bot.wz.plan_in_cancelled", nil), b.backKeyboard(loc, "home"))
		return
	case "review":
	default:
		b.reply(ctx, chatID, msgID, prefix+b.texts.T(loc, "bot.wz.plan_in_open_wait", venue), inline(
			[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_cancel_btn", nil), "cx", in.Id.String())},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}}))
		return
	}
	if in.Draft == nil {
		b.reply(ctx, chatID, msgID, prefix+b.texts.T(loc, "bot.wz.plan_in_open_wait", venue), b.backKeyboard(loc, "home"))
		return
	}
	n := in.Draft.N
	svg, err := b.arena.PlanIntakeDraftSVG(ctx, jwt, id.Current.OrgID, intakeID, n)
	if err != nil {
		b.logger.Warn("eventbot: plan draft not read", slog.String("error", err.Error()))
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	sum := in.Draft.Summary
	caption := prefix + b.texts.T(loc, "bot.wz.plan_in_summary", map[string]any{
		"Venue": Esc(in.VenueName), "Seats": sum.Seats, "Rows": sum.Rows, "Sections": sum.Sections,
	})
	if len(sum.Warnings) > 0 {
		caption += " " + b.texts.T(loc, "bot.wz.plan_in_warnings", map[string]any{"Count": len(sum.Warnings)})
	}
	caption += "\n\n" + b.texts.T(loc, "bot.wz.plan_in_question", nil)
	if in.ReviewRounds >= planReviewRoundsHint {
		caption += "\n\n" + b.texts.T(loc, "bot.wz.plan_in_rounds", map[string]any{"Count": in.ReviewRounds})
	}
	kb := inline(
		[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_ok_btn", nil), "ok", intakeID, strconv.Itoa(n))},
		[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_fix_btn", nil), "fix", intakeID, strconv.Itoa(n))},
		[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_admin_btn", nil), "adm", intakeID)},
	)
	if err := b.sendPlanPreview(ctx, chatID, svg, caption, kb); err != nil {
		b.logger.Warn("eventbot: plan preview not sent", slog.String("error", unwrapURLError(err).Error()))
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
	}
}

// sendPlanPreview sends the draft as a picture; a draft that cannot be drawn
// goes as its SVG file. The message carries the buttons and is a live screen.
func (b *Bot) sendPlanPreview(ctx context.Context, chatID int64, svg []byte, caption string, kb *models.InlineKeyboardMarkup) error {
	var sent *models.Message
	var err error
	g, _, issues := seating.ImportAnySVG(svg)
	var png []byte
	if len(issues) == 0 {
		png, err = planpreview.Render(g, planPreviewSide)
	} else {
		err = errors.New("draft does not import")
	}
	if err == nil {
		sent, err = b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileUpload{Filename: "plan.png", Data: bytes.NewReader(png)},
			Caption:     caption,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: kb,
		})
	} else {
		b.logger.Info("eventbot: plan draft sent as a file", slog.String("reason", err.Error()))
		sent, err = b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
			ChatID:      chatID,
			Document:    &models.InputFileUpload{Filename: "plan.svg", Data: bytes.NewReader(svg)},
			Caption:     caption,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: kb,
		})
	}
	if err != nil {
		return err
	}
	msgID := 0
	if sent != nil {
		msgID = sent.ID
	}
	b.feed.add(chatID, msgID)
	a := actionIn(ctx, chatID)
	if a != nil {
		a.own[msgID] = true
	}
	b.takeOver(ctx, chatID, a, 0)
	return nil
}

// planApprove approves draft n. A draft that is no longer the current one, or
// an intake that left "review", shows the screen again with a line about it.
func (b *Bot) planApprove(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, intakeID string, n int) {
	loc := id.Locale()
	in, err := b.arena.ApprovePlanIntake(ctx, jwt, id.Current.OrgID, intakeID, n)
	if err != nil {
		var ae *APIError
		switch {
		case errors.As(err, &ae) && ae.Status == http.StatusConflict:
			b.planReview(ctx, chatID, msgID, from, id, jwt, intakeID, b.texts.T(loc, "bot.wz.plan_in_changed", nil)+"\n\n")
		case errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
			b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_approve_bad", nil), inline(
				[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_admin_btn", nil), "adm", intakeID)}))
		default:
			b.ecError(ctx, chatID, msgID, id, err, "home")
		}
		return
	}
	b.planApproved(ctx, chatID, msgID, from, id, jwt, in)
}

// planApproved goes on after an approved intake: a wizard draft that waits for
// this intake gets the new plan at the head of its plan list; otherwise the
// person is told the plan is saved.
func (b *Bot) planApproved(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt string, in openapi.VenuePlanIntake) {
	loc := id.Locale()
	if in.PlanId == nil {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	ref, err := b.arena.PlanRefByID(ctx, jwt, *in.PlanId)
	if err != nil {
		b.logger.Warn("eventbot: approved plan not read", slog.String("error", err.Error()))
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	ws := b.wizardSession(id, jwt)
	d, _, derr := b.loadDraft(ctx, from.ID, ws)
	if derr == nil && d != nil && d.Step == stSPlanFile && d.PlanIntakeID == in.Id.String() {
		d.Paused = false
		report := b.texts.T(loc, "bot.wz.plan_in_saved_wizard", map[string]any{"Seats": ref.Seats})
		b.wizardApply(ctx, chatID, msgID, from, ws, d, WizInput{Plan: &PlanUploaded{Plan: ref, Report: report}})
		return
	}
	if derr == nil && d != nil && d.Step == stSPlan && d.session().VenueID == in.VenueId.String() {
		// The request was made from the plan question of this venue (a venue
		// that already had plans): the new plan joins the list.
		report := b.texts.T(loc, "bot.wz.plan_in_saved_wizard", map[string]any{"Seats": ref.Seats})
		b.wizardApply(ctx, chatID, msgID, from, ws, d, WizInput{Plan: &PlanUploaded{Plan: ref, Report: report}})
		return
	}
	b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_saved", map[string]any{"Seats": ref.Seats}), b.backKeyboard(loc, "home"))
}

// planRejectedScreen tells that an administrator could not build the plan.
func (b *Bot) planRejectedScreen(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, venue, intakeID string, reason *string) {
	loc := id.Locale()
	text, rows := b.planRejectedText(ctx, loc, id, jwt, from.ID, venue, intakeID, reason)
	b.reply(ctx, chatID, msgID, text, inline(rows...))
}

// planRejectedText is the "could not build" message with its buttons: send
// other files, and, when a wizard draft waits on this intake, go on without a
// plan.
func (b *Bot) planRejectedText(ctx context.Context, loc string, id *Identity, jwt string, tg int64, venue, intakeID string, reason *string) (string, [][]models.InlineKeyboardButton) {
	text := b.texts.T(loc, "bot.wz.plan_in_rejected_plain", map[string]any{"Venue": Esc(venue)})
	if reason != nil && strings.TrimSpace(*reason) != "" {
		text = b.texts.T(loc, "bot.wz.plan_in_rejected", map[string]any{"Venue": Esc(venue), "Reason": Esc(*reason)})
	}
	rows := [][]models.InlineKeyboardButton{{piBtn(b.texts.T(loc, "bot.wz.plan_in_again_btn", nil), "again", intakeID)}}
	if b.draftWaitsOn(ctx, id, jwt, tg, intakeID) != nil {
		rows = append(rows, []models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_skip_btn", nil), "skip", intakeID)})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
	return text, rows
}

// draftWaitsOn returns the person's wizard draft when it waits on the intake.
func (b *Bot) draftWaitsOn(ctx context.Context, id *Identity, jwt string, tg int64, intakeID string) *Draft {
	if id == nil || id.Current == nil || b.queries == nil {
		return nil
	}
	d, _, err := b.loadDraft(ctx, tg, b.wizardSession(id, jwt))
	if err != nil || d == nil || d.Step != stSPlanFile || d.PlanIntakeID != intakeID {
		return nil
	}
	return d
}

// planForgetIntake clears the intake from a wizard draft that waits on it.
func (b *Bot) planForgetIntake(ctx context.Context, tg int64, id *Identity, jwt, intakeID string) {
	if d := b.draftWaitsOn(ctx, id, jwt, tg, intakeID); d != nil {
		d.PlanIntakeID = ""
		d.Paused = false
		if _, err := b.storeDraft(ctx, tg, b.wizardSession(id, jwt), d); err != nil {
			b.logger.Warn("eventbot: wizard draft not freed from the intake", slog.String("error", err.Error()))
		}
	}
}

func (b *Bot) planEscalate(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, intakeID string) {
	loc := id.Locale()
	if _, err := b.arena.EscalatePlanIntake(ctx, jwt, id.Current.OrgID, intakeID); err != nil {
		if IsAPIError(err, http.StatusConflict) {
			b.planReview(ctx, chatID, msgID, from, id, jwt, intakeID, b.texts.T(loc, "bot.wz.plan_in_changed", nil)+"\n\n")
			return
		}
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_admin_called", nil), b.backKeyboard(loc, "home"))
}

// planAgain starts over with other files: the old intake is withdrawn when it
// is still open, and a wizard draft that waited on it asks for files again.
func (b *Bot) planAgain(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, intakeID string) {
	in, err := b.arena.GetPlanIntake(ctx, jwt, id.Current.OrgID, intakeID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if openPlanIntakeState(in.State) {
		if err := b.arena.CancelPlanIntake(ctx, jwt, id.Current.OrgID, intakeID); err != nil {
			b.logger.Warn("eventbot: old plan intake not cancelled", slog.String("error", err.Error()))
		}
	}
	ws := b.wizardSession(id, jwt)
	if d := b.draftWaitsOn(ctx, id, jwt, from.ID, intakeID); d != nil {
		d.PlanIntakeID = ""
		d.Paused = false
		if _, err := b.storeDraft(ctx, from.ID, ws, d); err != nil {
			b.wizardFail(ctx, chatID, msgID, id.Locale(), err)
			return
		}
		b.wizardRender(ctx, chatID, msgID, ws, d, "")
		return
	}
	b.planStartCollect(ctx, chatID, msgID, from, id, jwt, in.VenueId.String(), false)
}

// planSkip goes on without a plan: the wizard draft that waited on the intake
// takes the "continue without a plan" answer.
func (b *Bot) planSkip(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, intakeID string) {
	d := b.draftWaitsOn(ctx, id, jwt, from.ID, intakeID)
	if d == nil {
		b.showHome(ctx, chatID, msgID, from, "")
		return
	}
	d.Paused = false
	b.wizardApply(ctx, chatID, msgID, from, b.wizardSession(id, jwt), d, WizInput{Data: "plan:skip"})
}

// planSkipAsk is the answer to "Continue without a plan" while the draft still
// waits for an intake: the request is asked about once. A stale id (the intake
// is final or gone) is forgotten and the draft goes on at once.
func (b *Bot) planSkipAsk(ctx context.Context, chatID int64, msgID *int, from *models.User, ws WizSession, d *Draft) {
	if !b.syncDraftIntake(ctx, chatID, ws, d) {
		b.wizardApply(ctx, chatID, msgID, from, ws, d, WizInput{Data: "plan:skip"})
		return
	}
	loc := ws.Locale
	b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_skip_ask", nil), inline(
		[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_skip_yes_btn", nil), "sky", d.PlanIntakeID)},
		[]models.InlineKeyboardButton{piBtn(b.texts.T(loc, "bot.wz.plan_in_skip_no_btn", nil), "skn", d.PlanIntakeID)},
	))
}

// planSkipCancel is "Yes" to the question above: the request is cancelled and
// the draft goes on without a plan. A request that is no longer open is not
// reported as cancelled: its real state decides (an approved one is the plan
// the draft was waiting for).
func (b *Bot) planSkipCancel(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, intakeID string) {
	if err := b.arena.CancelPlanIntake(ctx, jwt, id.Current.OrgID, intakeID); err != nil {
		switch {
		case IsAPIError(err, http.StatusConflict):
			in, gerr := b.arena.GetPlanIntake(ctx, jwt, id.Current.OrgID, intakeID)
			switch {
			case gerr != nil:
				b.ecError(ctx, chatID, msgID, id, gerr, "home")
				return
			case in.State == "approved":
				b.planApproved(ctx, chatID, msgID, from, id, jwt, in)
				return
			case openPlanIntakeState(in.State):
				b.planReview(ctx, chatID, msgID, from, id, jwt, intakeID, "")
				return
			}
			// Cancelled, rejected: nothing is open any more, the draft goes on.
		case IsAPIError(err, http.StatusNotFound):
		default:
			b.ecError(ctx, chatID, msgID, id, err, "home")
			return
		}
	}
	b.planSkip(ctx, chatID, msgID, from, id, jwt, intakeID)
}

// syncDraftIntake reads the real state of the intake a draft waits for. It
// reports whether the draft still waits: an open intake keeps the waiting
// screen (its state is kept for the buttons), a final or missing one is
// forgotten and the draft is freed. An answer that cannot be read keeps the
// draft as it is.
func (b *Bot) syncDraftIntake(ctx context.Context, tg int64, ws WizSession, d *Draft) bool {
	if d.PlanIntakeID == "" {
		return false
	}
	in, err := b.arena.GetPlanIntake(ctx, ws.JWT, ws.OrgID, d.PlanIntakeID)
	switch {
	case err == nil && openPlanIntakeState(in.State):
		if d.PlanIntakeState != in.State {
			d.PlanIntakeState = in.State
			if _, serr := b.storeDraft(ctx, tg, ws, d); serr != nil {
				b.logger.Warn("eventbot: wizard draft intake state not stored", slog.String("error", serr.Error()))
			}
		}
		return true
	case err == nil, IsAPIError(err, http.StatusNotFound), IsAPIError(err, http.StatusBadRequest):
		d.PlanIntakeID, d.PlanIntakeState, d.Paused = "", "", false
		if _, serr := b.storeDraft(ctx, tg, ws, d); serr != nil {
			b.logger.Warn("eventbot: wizard draft not freed from the intake", slog.String("error", serr.Error()))
		}
		return false
	}
	b.logger.Warn("eventbot: plan intake state not read", slog.String("error", err.Error()))
	return true
}

// ─── notices ─────────────────────────────────────────────────────────────────

// planNoticeEvery is how often the bot asks for plan notices.
const planNoticeEvery = 30 * time.Second

// planNoticeLoop tells the organizers what happened to their intakes, each
// notice once.
func (b *Bot) planNoticeLoop(ctx context.Context) {
	every := b.noticeEvery
	if every <= 0 {
		every = planNoticeEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.planAnnounce(ctx)
		}
	}
}

func (b *Bot) planAnnounce(ctx context.Context) {
	notices, err := b.arena.ClaimVenuePlanNotices(ctx)
	if err != nil {
		b.logger.Warn("eventbot: plan notices failed", slog.String("error", err.Error()))
		return
	}
	for _, n := range notices {
		for _, tg := range n.TelegramUserIds {
			b.planNoticeSafely(ctx, tg, n)
		}
	}
}

// planNoticeSafely tells one chat; whatever goes wrong there is logged and the
// rest of the batch goes on (each notice is claimed once and never comes back).
func (b *Bot) planNoticeSafely(ctx context.Context, tg int64, n openapi.BotVenuePlanNotice) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("eventbot: plan notice panicked", slog.Int64("telegram_user_id", tg), slog.Any("panic", r))
		}
	}()
	do := func(c context.Context) {
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error("eventbot: plan notice panicked", slog.Int64("telegram_user_id", tg), slog.Any("panic", r))
			}
		}()
		tell := b.planNoticeTo
		if b.noticeSeam != nil {
			tell = b.noticeSeam
		}
		tell(c, tg, n)
	}
	if err := b.serial.do(detached(ctx), tg, do); err != nil {
		b.logger.Warn("eventbot: plan notice not told", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// planNoticeTo tells one chat about one notice, in that person's language.
func (b *Bot) planNoticeTo(ctx context.Context, tg int64, n openapi.BotVenuePlanNotice) {
	link, err := b.queries.GetBotTelegramLink(ctx, tg)
	if err != nil || link.RevokedAt != nil {
		return
	}
	id, jwt, ierr := b.resolveIdentity(ctx, tg)
	if ierr != nil {
		id, jwt = nil, ""
	}
	b.planNoticeSay(ctx, tg, link.Locale, link, id, jwt, n)
}

// orgPrefix is the first eight bytes of an organization id as hex: the notice
// button of an organization that is not the current one carries it (Telegram's
// 64 bytes leave no room for the whole id next to the intake id).
func orgPrefix(org uuid.UUID) string { return fmt.Sprintf("%x", org[:8]) }

// orgNameOf is the name of one of the person's organizations ("" when the
// person has no such organization).
func orgNameOf(id *Identity, org uuid.UUID) string {
	if id == nil {
		return ""
	}
	for _, m := range id.Memberships {
		if m.OrgID == org {
			return m.OrgName
		}
	}
	return ""
}

// planNoticeSay is the message of a notice. A notice of an organization that is
// not the person's current one names the organization, and its button does not
// open the request (it says where the request lives).
func (b *Bot) planNoticeSay(ctx context.Context, tg int64, locale string, link gen.BotTelegramLinkRow, id *Identity, jwt string, n openapi.BotVenuePlanNotice) {
	loc := NormalizeLocale(locale)
	venue := Esc(n.VenueName)
	intake := n.IntakeId.String()
	note := ""
	if n.Note != nil {
		note = strings.TrimSpace(*n.Note)
	}
	current := id != nil && id.Current != nil && id.Current.OrgID == n.OrgId
	known := id != nil && id.Current != nil
	orgLine := ""
	if known && !current {
		if name := orgNameOf(id, n.OrgId); name != "" {
			orgLine = b.texts.T(loc, "bot.wz.plan_in_notice_org", map[string]any{"Org": Esc(name)}) + "\n\n"
		}
	}
	switch string(n.Kind) {
	case "returned", "draft_ready":
		key, vars := "bot.wz.plan_in_ready", map[string]any{"Venue": venue}
		if note != "" {
			key, vars = "bot.wz.plan_in_returned", map[string]any{"Venue": venue, "Note": Esc(note)}
		}
		btn := piBtn(b.texts.T(loc, "bot.wz.plan_in_check_btn", nil), "rv", intake)
		if known && !current {
			btn = piBtn(b.texts.T(loc, "bot.wz.plan_in_check_btn", nil), "ro", orgPrefix(n.OrgId), intake)
		}
		b.send(ctx, tg, orgLine+b.texts.T(loc, key, vars), inline([]models.InlineKeyboardButton{btn}))
	case "rejected":
		if known && !current {
			text := b.texts.T(loc, "bot.wz.plan_in_rejected_plain", map[string]any{"Venue": venue})
			if note != "" {
				text = b.texts.T(loc, "bot.wz.plan_in_rejected", map[string]any{"Venue": venue, "Reason": Esc(note)})
			}
			b.send(ctx, tg, orgLine+text, b.backKeyboard(loc, "home"))
			return
		}
		if !known {
			// Nothing to resume: the message still carries the way on.
			id, jwt = &Identity{Link: link, Current: &Membership{OrgID: n.OrgId}}, ""
		}
		text, rows := b.planRejectedText(ctx, loc, id, jwt, tg, n.VenueName, intake, n.Note)
		b.send(ctx, tg, text, inline(rows...))
	case "approved":
		if !current {
			b.send(ctx, tg, orgLine+b.texts.T(loc, "bot.wz.plan_in_saved_by_admin", map[string]any{"Venue": venue}), b.backKeyboard(loc, "home"))
			return
		}
		in, gerr := b.arena.GetPlanIntake(ctx, jwt, n.OrgId, intake)
		if gerr != nil {
			b.logger.Warn("eventbot: approved intake not read", slog.String("error", gerr.Error()))
			b.send(ctx, tg, b.texts.T(loc, "bot.wz.plan_in_saved_by_admin", map[string]any{"Venue": venue}), b.backKeyboard(loc, "home"))
			return
		}
		from := &models.User{ID: tg, LanguageCode: link.Locale}
		b.planApproved(ctx, tg, nil, from, id, jwt, in)
	}
}

// planOtherOrg answers the button of a notice of another organization
// ("pi:ro:<org prefix>:<intake>"). When the person has switched to that
// organization since, the request opens; otherwise they are told where it lives.
func (b *Bot) planOtherOrg(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, arg string) {
	loc := id.Locale()
	prefix, intake, _ := strings.Cut(arg, ":")
	if orgPrefix(id.Current.OrgID) == prefix {
		b.planReview(ctx, chatID, msgID, from, id, jwt, intake, "")
		return
	}
	for _, m := range id.Memberships {
		if orgPrefix(m.OrgID) == prefix {
			b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_other_org", map[string]any{"Org": Esc(m.OrgName)}), b.backKeyboard(loc, "home"))
			return
		}
	}
	b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.wz.plan_in_gone", nil), b.backKeyboard(loc, "home"))
}

// ─── after the event is saved ────────────────────────────────────────────────

// intakeSplitRows are the "Split seats by price" buttons of the saved dates
// that are sold by a plan an intake of this draft made. A date is offered once.
func (b *Bot) intakeSplitRows(loc string, d *Draft) [][]models.InlineKeyboardButton {
	if len(d.IntakePlans) == 0 {
		return nil
	}
	eventID, err := uuid.Parse(d.Saved.EventID)
	if err != nil {
		return nil
	}
	fromIntake := map[string]bool{}
	for _, v := range d.IntakePlans {
		fromIntake[v] = true
	}
	var rows [][]models.InlineKeyboardButton
	for i, s := range d.Sessions {
		if !s.seated() || !fromIntake[s.PlanVersionID] || i >= len(d.Saved.Sessions) {
			continue
		}
		sessionID, err := uuid.Parse(d.Saved.Sessions[i].SessionID)
		if err != nil {
			continue
		}
		bt := b.splitSeatsByPriceButton(loc, eventID, sessionID)
		label := bt.Label
		if len(d.Sessions) > 1 {
			label = fmt.Sprintf("%s (%s %s)", label, DisplayDate(s.Date), s.Time)
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(label, 60), CallbackData: "sl:" + bt.Data}})
	}
	return rows
}
