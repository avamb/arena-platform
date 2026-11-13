package eventbot

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

func TestSplitIntakeDraft(t *testing.T) {
	t.Parallel()
	id := uuid.NewString()
	if got, n, ok := splitIntakeDraft(id + ":3"); got != id || n != 3 || !ok {
		t.Fatalf("got %q %d %v", got, n, ok)
	}
	for _, bad := range []string{id, id + ":", id + ":x", id + ":0", id + ":-1"} {
		if _, _, ok := splitIntakeDraft(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A document keeps its name, a photo is its largest size.
func TestPlanFileOf(t *testing.T) {
	t.Parallel()
	doc := &models.Message{Document: &models.Document{FileID: "d", FileName: "hall.pdf", FileSize: 10}}
	if f, ok := planFileOf(doc, 1); !ok || f.FileID != "d" || f.Name != "hall.pdf" || f.Photo {
		t.Fatalf("document = %+v %v", f, ok)
	}
	unnamed := &models.Message{Document: &models.Document{FileID: "d", FileSize: 10}}
	if f, _ := planFileOf(unnamed, 4); f.Name != "file-4" {
		t.Fatalf("unnamed document = %+v", f)
	}
	photo := &models.Message{Photo: []models.PhotoSize{{FileID: "s", Width: 90}, {FileID: "l", Width: 1280, FileSize: 99}, {FileID: "m", Width: 320}}}
	if f, ok := planFileOf(photo, 2); !ok || f.FileID != "l" || f.Size != 99 || !f.Photo || f.Name != "photo-2.jpg" {
		t.Fatalf("photo = %+v %v", f, ok)
	}
	if _, ok := planFileOf(&models.Message{Text: "x"}, 1); ok {
		t.Fatal("a text is no file")
	}
}

// Every callback of the feature fits Telegram's 64 bytes.
func TestPlanIntakeCallbacksFit(t *testing.T) {
	t.Parallel()
	id := uuid.NewString()
	for _, data := range []string{
		planDataPrefix + "up:" + id,
		planDataPrefix + "rv:" + id,
		planDataPrefix + "ok:" + id + ":999",
		planDataPrefix + "fix:" + id + ":999",
		planDataPrefix + "adm:" + id,
		planDataPrefix + "again:" + id,
		planDataPrefix + "skip:" + id,
		planDataPrefix + "cx:" + id,
		planDataPrefix + "sky:" + id,
		planDataPrefix + "skn:" + id,
		planDataPrefix + "rm:10",
		planDataPrefix + "ro:" + orgPrefix(uuid.New()) + ":" + id,
	} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
	}
}

// Only the dates sold by a plan an intake of this draft made are offered
// "Split seats by price", each with its saved session.
func TestIntakeSplitRows(t *testing.T) {
	t.Parallel()
	b := &Bot{texts: NewTexts(nil)}
	event, s1, s2 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	d := &Draft{
		IntakePlans: []string{"v-intake"},
		Sessions: []DraftSession{
			{PlanVersionID: "v-intake", Date: "2099-12-15", Time: "20:00"},
			{PlanVersionID: "v-other", Date: "2099-12-16", Time: "20:00"},
			{PlanVersionID: "v-intake", Date: "2099-12-17", Time: "20:00"},
		},
		Saved: DraftSaved{EventID: event, Sessions: []SavedSession{{SessionID: s1}, {SessionID: s2}, {SessionID: s2}}},
	}
	rows := b.intakeSplitRows("en", d)
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if got := rows[0][0].CallbackData; got != "sl:o:"+s1 {
		t.Errorf("first callback = %q", got)
	}
	if !strings.Contains(rows[0][0].Text, "2099") && !strings.Contains(rows[0][0].Text, "15") {
		t.Errorf("a multi-date event names the date: %q", rows[0][0].Text)
	}
	d.IntakePlans = nil
	if rows := b.intakeSplitRows("en", d); rows != nil {
		t.Fatalf("no intake plan, rows = %v", rows)
	}
}
