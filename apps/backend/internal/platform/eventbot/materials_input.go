package eventbot

// materials_input.go — what the person SENDS to the Materials screens
// (task 7-3): a typed link, caption, seller name or coordinates, a photo (or a
// picture sent as a file) and a Telegram location. Each is accepted only at
// the step of the dialog that waits for it (materials.go), so a stray photo
// never lands in the gallery: the dialog ends with any other screen's press
// and with every command.

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg" // registers the decoder image.DecodeConfig needs
	_ "image/png"  // registers the decoder image.DecodeConfig needs
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// materialsWaiting reports whether the step waits for something the person
// sends (as opposed to a button).
func materialsWaiting(step string) bool {
	return step == mtStepAdd || step == mtStepCaption || step == mtStepSeller
}

// materialsInputContext loads the dialog for an incoming message and resolves
// the person. ok is false when the message is not for this dialog (none open,
// not waiting for input, another organization, no right to change) and the
// caller must let it fall through. When the dialog ran out while waiting, the
// person is told once and the message counts as handled.
func (b *Bot) materialsInputContext(ctx context.Context, chatID int64, from *models.User) (st materialsDialog, step string, id *Identity, jwt string, sec materialSection, handled, ok bool) {
	st, step, found, expired := b.loadMaterialsState(ctx, from.ID)
	if expired && materialsWaiting(step) {
		loc := NormalizeLocale(from.LanguageCode)
		if idn, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = idn.Locale()
		}
		kb := b.backKeyboard(loc, "events")
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), kb)
		return st, step, nil, "", sec, true, false
	}
	if !found || !materialsWaiting(step) {
		return st, step, nil, "", sec, false, false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canEditMaterials(id) {
		b.leaveMaterials(ctx, from.ID)
		return st, step, nil, "", sec, false, false
	}
	sec, okSec := materialSectionByCode(st.Section)
	if !okSec {
		b.leaveMaterials(ctx, from.ID)
		return st, step, nil, "", sec, false, false
	}
	return st, step, id, jwt, sec, false, true
}

// materialsText consumes a typed text for a question in progress. It reports
// whether the text was taken.
func (b *Bot) materialsText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	st, step, id, jwt, sec, handled, ok := b.materialsInputContext(ctx, chatID, from)
	if handled {
		return true
	}
	if !ok {
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	switch step {
	case mtStepCaption:
		caption := cleanMaterialText(text)
		if caption == "" || runeLen(caption) > maxMaterialCaptionRunes {
			b.materialsAskText(ctx, chatID, edit, id, st, sec, mtStepCaption, "bot.mt.ask_caption", b.texts.T(loc, "bot.mt.err_caption", nil)+"\n\n")
			return true
		}
		if st.OpenID == nil {
			b.leaveMaterials(ctx, from.ID)
			return false
		}
		if _, err := b.arena.MaterialsPatch(ctx, jwt, st.OrgID, st.EventID, sec, *st.OpenID, map[string]any{"caption": caption}); err != nil {
			b.materialsInputFail(ctx, chatID, edit, id, jwt, st, sec, step, "bot.mt.ask_caption", err)
			return true
		}
		b.materialsItem(ctx, chatID, edit, id, jwt, st, sec, *st.OpenID, b.texts.T(loc, "bot.mt.saved", nil)+"\n\n")
	case mtStepSeller:
		seller := cleanMaterialText(text)
		if seller == "" || runeLen(seller) > maxMaterialSellerRunes {
			b.materialsAskText(ctx, chatID, edit, id, st, sec, mtStepSeller, "bot.mt.ask_seller", b.texts.T(loc, "bot.mt.err_seller", nil)+"\n\n")
			return true
		}
		if st.OpenID == nil {
			b.leaveMaterials(ctx, from.ID)
			return false
		}
		if _, err := b.arena.MaterialsPatch(ctx, jwt, st.OrgID, st.EventID, sec, *st.OpenID, map[string]any{"seller_name": seller}); err != nil {
			b.materialsInputFail(ctx, chatID, edit, id, jwt, st, sec, step, "bot.mt.ask_seller", err)
			return true
		}
		b.materialsItem(ctx, chatID, edit, id, jwt, st, sec, *st.OpenID, b.texts.T(loc, "bot.mt.saved", nil)+"\n\n")
	case mtStepAdd:
		b.materialsAddText(ctx, chatID, edit, id, jwt, st, sec, text)
	}
	return true
}

// materialsAddText handles the text sent at "Add": a link for videos and
// partner links, coordinates for the map. A photo part wants a picture.
func (b *Bot) materialsAddText(ctx context.Context, chatID int64, edit *int, id *Identity, jwt string, st materialsDialog, sec materialSection, text string) {
	loc := id.Locale()
	askKey := "bot.mt.ask_" + sec.Code
	switch sec.Code {
	case "v", "p":
		link := normalizeMaterialLink(text)
		if _, err := b.arena.MaterialsAdd(ctx, jwt, st.OrgID, st.EventID, sec, map[string]any{"url": link}); err != nil {
			b.materialsInputFail(ctx, chatID, edit, id, jwt, st, sec, mtStepAdd, askKey, err)
			return
		}
		b.materialsList(ctx, chatID, edit, id, jwt, st, sec, b.texts.T(loc, "bot.mt.added", nil)+"\n\n")
	case "m":
		lat, lon, ok := ParseCoordinates(text)
		if !ok {
			b.materialsAskText(ctx, chatID, edit, id, st, sec, mtStepAdd, askKey, b.texts.T(loc, "bot.mt.err_map", nil)+"\n\n")
			return
		}
		b.materialsSetPoint(ctx, chatID, edit, id, jwt, st, sec, lat, lon)
	default: // a photo part
		b.materialsAskText(ctx, chatID, edit, id, st, sec, mtStepAdd, askKey, b.texts.T(loc, "bot.mt.err_need_photo", nil)+"\n\n")
	}
}

// materialsSetPoint saves the map point and shows the map.
func (b *Bot) materialsSetPoint(ctx context.Context, chatID int64, edit *int, id *Identity, jwt string, st materialsDialog, sec materialSection, lat, lon float64) {
	if _, err := b.arena.MapPointSet(ctx, jwt, st.OrgID, st.EventID, lat, lon); err != nil {
		b.materialsInputFail(ctx, chatID, edit, id, jwt, st, sec, mtStepAdd, "bot.mt.ask_m", err)
		return
	}
	b.materialsList(ctx, chatID, edit, id, jwt, st, sec, b.texts.T(id.Locale(), "bot.mt.saved", nil)+"\n\n")
}

// materialsInputFail answers a refused change made from a question: a refusal
// the bot has words for keeps the question open with the reason above it
// (a full part goes back to the list), anything else is the generic error.
func (b *Bot) materialsInputFail(ctx context.Context, chatID int64, edit *int, id *Identity, jwt string, st materialsDialog, sec materialSection, step, askKey string, err error) {
	key := materialsErrKey(err)
	switch key {
	case "":
		b.ecError(ctx, chatID, edit, id, err, "mt:m")
	case "bot.mt.err_full", "bot.mt.err_gone", "bot.mt.err_changed":
		b.materialsFail(ctx, chatID, edit, id, jwt, st, sec, err)
	default:
		b.materialsAskText(ctx, chatID, edit, id, st, sec, step, askKey, b.texts.T(id.Locale(), key, nil)+"\n\n")
	}
}

// materialsLocation takes a Telegram location sent while the map question is
// open. It reports whether the message was taken.
func (b *Bot) materialsLocation(ctx context.Context, chatID int64, from *models.User, loc *models.Location) bool {
	st, step, id, jwt, sec, handled, ok := b.materialsInputContext(ctx, chatID, from)
	if handled {
		return true
	}
	if !ok || step != mtStepAdd || sec.Code != "m" || loc == nil {
		return false
	}
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	lat, lon := math.Round(loc.Latitude*1e6)/1e6, math.Round(loc.Longitude*1e6)/1e6
	b.materialsSetPoint(ctx, chatID, edit, id, jwt, st, sec, lat, lon)
	return true
}

// materialsPhoto takes a picture sent while a photo part's "Add" is open: a
// Telegram photo, or an image sent as a file. It reports whether the message
// was taken. After a photo is added the dialog keeps waiting, so an album (one
// message per picture) fills the part one picture after another.
func (b *Bot) materialsPhoto(ctx context.Context, chatID int64, from *models.User, m *models.Message) bool {
	st, step, id, jwt, sec, handled, ok := b.materialsInputContext(ctx, chatID, from)
	if handled {
		return true
	}
	if !ok {
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if step != mtStepAdd || !sec.isPhoto() {
		// A picture where a text is asked: say what is wanted instead.
		key, ask := "bot.mt.err_need_text", "bot.mt.ask_"+sec.Code
		switch step {
		case mtStepCaption:
			ask = "bot.mt.ask_caption"
		case mtStepSeller:
			ask = "bot.mt.ask_seller"
		}
		b.materialsAskText(ctx, chatID, edit, id, st, sec, step, ask, b.texts.T(loc, key, nil)+"\n\n")
		return true
	}

	fileID, fileName, fileSize := materialFileOf(m)
	if fileID == "" {
		return false
	}
	askKey := "bot.mt.ask_" + sec.Code
	again := func(key string) {
		b.materialsAskText(ctx, chatID, edit, id, withOpenCleared(st), sec, mtStepAdd, askKey, b.texts.T(loc, key, nil)+"\n\n")
	}
	if fileSize > materialPhotoMaxBytes {
		again("bot.mt.err_photo_big")
		return true
	}
	// Check the part has room BEFORE the picture is downloaded and stored.
	l, err := b.arena.MaterialsList(ctx, jwt, st.OrgID, st.EventID, sec)
	if err != nil {
		b.ecError(ctx, chatID, edit, id, err, "mt:m")
		return true
	}
	if len(l.Items) >= materialsMax(l, sec) {
		b.materialsList(ctx, chatID, edit, id, jwt, st, sec, b.texts.T(loc, "bot.mt.err_full", map[string]any{"Max": materialsMax(l, sec)})+"\n\n")
		return true
	}
	data, err := b.downloadTelegramFile(ctx, fileID)
	if err != nil {
		b.logger.Warn("eventbot: material photo download failed", slog.String("error", err.Error()))
		again("bot.mt.err_photo_failed")
		return true
	}
	if len(data) > materialPhotoMaxBytes {
		again("bot.mt.err_photo_big")
		return true
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		again("bot.mt.err_photo_type")
		return true
	}
	contentType := "image/jpeg"
	if format == "png" {
		contentType = "image/png"
	}
	if fileName == "" {
		fileName = "photo." + format
	}
	up, err := b.arena.UploadMedia(ctx, jwt, st.OrgID, sec.OwnerType, fileName, contentType, data)
	if err != nil {
		b.logger.Warn("eventbot: material photo upload failed", slog.String("error", err.Error()))
		again("bot.mt.err_photo_failed")
		return true
	}
	mediaID, perr := uuid.Parse(up.ID)
	if perr != nil {
		again("bot.mt.err_photo_failed")
		return true
	}
	body := map[string]any{"media_id": mediaID.String()}
	if caption := cleanMaterialText(m.Caption); caption != "" && runeLen(caption) <= maxMaterialCaptionRunes {
		body["caption"] = caption
	}
	if _, err := b.arena.MaterialsAdd(ctx, jwt, st.OrgID, st.EventID, sec, body); err != nil {
		b.materialsInputFail(ctx, chatID, edit, id, jwt, st, sec, mtStepAdd, askKey, err)
		return true
	}
	// The part keeps waiting for the next picture until it is full.
	count := len(l.Items) + 1
	if count >= materialsMax(l, sec) {
		b.materialsList(ctx, chatID, edit, id, jwt, st, sec, b.texts.T(loc, "bot.mt.added", nil)+"\n\n")
		return true
	}
	prefix := b.texts.T(loc, "bot.mt.added_more", map[string]any{"N": count, "Max": materialsMax(l, sec)}) + "\n\n"
	b.materialsAskPhoto(ctx, chatID, edit, id, st, sec, prefix)
	return true
}

// materialsAskPhoto re-opens the photo question after a picture was added,
// with a "Done" button that shows the list.
func (b *Bot) materialsAskPhoto(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st materialsDialog, sec materialSection, prefix string) {
	loc := id.Locale()
	st.Section, st.OpenID = sec.Code, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveMaterialsState(ctx, id.Link.TelegramUserID, st, mtStepAdd)
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.mt.done_btn", nil), CallbackData: "mt:s:" + sec.Code}},
	}}
	b.reply(ctx, chatID, editMsgID, prefix+b.texts.T(loc, "bot.mt.ask_more_"+sec.Code, nil), kb)
}

// withOpenCleared returns the state without an open entry, for an "Add"
// question that must not offer the way back to a card.
func withOpenCleared(st materialsDialog) materialsDialog {
	st.OpenID = nil
	return st
}

// materialFileOf picks the file of a message: the largest size of a photo, or
// the document. The name is what Telegram reports (empty for a photo).
func materialFileOf(m *models.Message) (fileID, name string, size int64) {
	switch {
	case m.Document != nil:
		return m.Document.FileID, m.Document.FileName, m.Document.FileSize
	case len(m.Photo) > 0:
		largest := m.Photo[0]
		for _, p := range m.Photo[1:] {
			if p.Width > largest.Width {
				largest = p
			}
		}
		return largest.FileID, "photo.jpg", int64(largest.FileSize)
	}
	return "", "", 0
}

// ParseCoordinates reads "latitude, longitude" as a person types or copies it:
// "50.0755, 14.4378", "50.0755 14.4378", "50.0755,14.4378", a pair with a
// semicolon, or a decimal comma ("50,0755; 14,4378"). It reports false for
// anything else and for a point outside -90..90 / -180..180.
func ParseCoordinates(raw string) (lat, lon float64, ok bool) {
	s := strings.TrimSpace(raw)
	var parts []string
	switch {
	case strings.Contains(s, ";"):
		parts = strings.Split(s, ";")
	case len(strings.Fields(s)) == 2:
		parts = strings.Fields(s)
	case strings.Count(s, ",") == 1:
		parts = strings.Split(s, ",")
	default:
		return 0, 0, false
	}
	if len(parts) != 2 {
		return 0, 0, false
	}
	num := func(p string) (float64, bool) {
		p = strings.Trim(strings.TrimSpace(p), ",;")
		p = strings.TrimSuffix(strings.TrimSpace(p), "°")
		if strings.Count(p, ",") == 1 && !strings.Contains(p, ".") {
			p = strings.Replace(p, ",", ".", 1)
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	}
	la, ok1 := num(parts[0])
	lo, ok2 := num(parts[1])
	if !ok1 || !ok2 || la < -90 || la > 90 || lo < -180 || lo > 180 {
		return 0, 0, false
	}
	return math.Round(la*1e6) / 1e6, math.Round(lo*1e6) / 1e6, true
}
