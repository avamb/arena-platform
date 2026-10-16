package eventbot

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

func TestParseCoordinates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		lat, lon float64
		ok       bool
	}{
		{"50.0755, 14.4378", 50.0755, 14.4378, true},
		{"50.0755 14.4378", 50.0755, 14.4378, true},
		{"50.0755,14.4378", 50.0755, 14.4378, true},
		{"  50.0755;14.4378 ", 50.0755, 14.4378, true},
		{"50,0755 14,4378", 50.0755, 14.4378, true},
		{"50,0755; 14,4378", 50.0755, 14.4378, true},
		{"50,0755, 14,4378", 50.0755, 14.4378, true},
		{"-33.8688, 151.2093", -33.8688, 151.2093, true},
		{"50.07553812, 14.43781299", 50.075538, 14.437813, true},
		{"90, 180", 90, 180, true},
		{"91, 10", 0, 0, false},
		{"10, 181", 0, 0, false},
		{"50.0755", 0, 0, false},
		{"50.0755, 14.4378, 12", 0, 0, false},
		{"50,0755,14,4378", 0, 0, false},
		{"north, east", 0, 0, false},
		{"NaN, 1", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		lat, lon, ok := ParseCoordinates(c.in)
		if ok != c.ok || (ok && (lat != c.lat || lon != c.lon)) {
			t.Errorf("ParseCoordinates(%q) = (%v, %v, %v), want (%v, %v, %v)", c.in, lat, lon, ok, c.lat, c.lon, c.ok)
		}
	}
}

func TestMaterialsErrKey(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]string{
		"event.gallery_full":                 "bot.mt.err_full",
		"event.videos_full":                  "bot.mt.err_full",
		"event.partner_links_full":           "bot.mt.err_full",
		"event.report_photos_full":           "bot.mt.err_full",
		"event.invalid_media":                "bot.mt.err_photo_failed",
		"event.invalid_report_media":         "bot.mt.err_photo_failed",
		"event.invalid_caption":              "bot.mt.err_caption",
		"event.invalid_video_url":            "bot.mt.err_video_url",
		"event.invalid_partner_url":          "bot.mt.err_partner_url",
		"event.invalid_seller_name":          "bot.mt.err_seller",
		"event.invalid_map_point":            "bot.mt.err_map",
		"event.report_photos_duplicate":      "bot.mt.err_duplicate",
		"event.gallery_item_not_found":       "bot.mt.err_gone",
		"event.photo_not_found":              "bot.mt.err_gone",
		"event.gallery_order_mismatch":       "bot.mt.err_changed",
		"event.partner_links_order_mismatch": "bot.mt.err_changed",
		"event.not_found":                    "",
	} {
		if got := materialsErrKey(&APIError{Status: 409, Code: code}); got != want {
			t.Errorf("%s -> %q, want %q", code, got, want)
		}
	}
	if materialsErrKey(errors.New("boom")) != "" {
		t.Error("a transport error has no words of its own")
	}
}

func TestNormalizeMaterialLink(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"  https://youtu.be/abc ":            "https://youtu.be/abc",
		"youtu.be/abc":                       "https://youtu.be/abc",
		"ticketportal.cz/event/12":           "https://ticketportal.cz/event/12",
		"http://example.com/x":               "http://example.com/x", // the API refuses it in its own words
		"two words.cz":                       "two words.cz",
		"nodots":                             "nodots",
		"":                                   "",
		"ftp://files.example.com/a":          "ftp://files.example.com/a",
		"https://sub.example.com/a?b=1#frag": "https://sub.example.com/a?b=1#frag",
	} {
		if got := normalizeMaterialLink(in); got != want {
			t.Errorf("normalizeMaterialLink(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOSMLink(t *testing.T) {
	t.Parallel()
	got := osmLink(50.0755, 14.4378)
	if !strings.HasPrefix(got, "https://www.openstreetmap.org/?mlat=50.075500&mlon=14.437800#map=17/") {
		t.Errorf("osmLink = %s", got)
	}
}

func TestMaterialFileOf(t *testing.T) {
	t.Parallel()
	photo := &models.Message{Photo: []models.PhotoSize{
		{FileID: "small", Width: 90, FileSize: 100},
		{FileID: "large", Width: 1280, FileSize: 900},
		{FileID: "mid", Width: 320, FileSize: 400},
	}}
	if id, name, size := materialFileOf(photo); id != "large" || name != "photo.jpg" || size != 900 {
		t.Errorf("photo = (%s, %s, %d)", id, name, size)
	}
	doc := &models.Message{Document: &models.Document{FileID: "doc", FileName: "pic.png", FileSize: 77}}
	if id, name, size := materialFileOf(doc); id != "doc" || name != "pic.png" || size != 77 {
		t.Errorf("document = (%s, %s, %d)", id, name, size)
	}
	if id, _, _ := materialFileOf(&models.Message{}); id != "" {
		t.Error("a message with no file has no file id")
	}
}

func TestMaterialSections(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, s := range materialSections {
		if seen[s.Code] || s.Path == "" || s.Max <= 0 {
			t.Errorf("bad section %+v", s)
		}
		seen[s.Code] = true
		if got, ok := materialSectionByCode(s.Code); !ok || got.Path != s.Path {
			t.Errorf("section %q is not found by its code", s.Code)
		}
		if s.Code != "m" && s.OrderKey == "" {
			t.Errorf("list section %q needs an order key", s.Code)
		}
		if s.isPhoto() != (s.Code == "g" || s.Code == "r") {
			t.Errorf("section %q: isPhoto = %v", s.Code, s.isPhoto())
		}
	}
	if _, ok := materialSectionByCode("x"); ok {
		t.Error("an unknown code must not resolve")
	}
}

// The menu's labels and every per-part text exist in every language for every
// part (the dynamic keys the code builds from a part's code).
func TestMaterialsDynamicKeysExist(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		for _, s := range materialSections {
			keys := []string{"bot.mt.hint_" + s.Code, "bot.mt.ask_" + s.Code, "bot.mt.del_ask_" + s.Code}
			if s.Code != "m" {
				keys = append(keys, "bot.mt.sec_"+s.Code, "bot.mt.list_"+s.Code, "bot.mt.empty_"+s.Code)
			}
			if s.isPhoto() {
				keys = append(keys, "bot.mt.ask_more_"+s.Code, "bot.mt.card_photo_"+s.Code)
			}
			for _, k := range keys {
				if got := b.texts.T(loc, k, map[string]any{"N": 1, "Max": 6, "State": "x", "Event": "e", "Total": 2, "Caption": "c"}); got == "" || got == k {
					t.Errorf("[%s] %s renders %q", loc, k, got)
				}
			}
		}
	}
}

// Every press of the dialog belongs to it, and its dialog is ended by any
// other screen's press (the dialog tables in dialog_owners.go).
func TestMaterialsDialogOwnsItsPresses(t *testing.T) {
	t.Parallel()
	o, ok := dialogOwners[materialsDialogKind]
	if !ok {
		t.Fatal("the materials dialog is not in dialogOwners")
	}
	for _, d := range []string{"mt:m", "mt:s:g", "mt:o:0", "mt:u", "mt:y"} {
		if !o.owns(d) {
			t.Errorf("%q must belong to the materials dialog", d)
		}
	}
	for _, d := range []string{"ec:mt:" + uuid.NewString(), "pr:l", "home"} {
		if o.owns(d) {
			t.Errorf("%q must not belong to the materials dialog", d)
		}
	}
}
