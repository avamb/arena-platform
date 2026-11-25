package eventbot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func metaTestBot(t *testing.T, status int, body string) *Bot {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/integrations/meta") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &Bot{
		texts:          NewTexts(bundle),
		arena:          NewArenaClient(srv.URL, "", nil),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCenterURL: "https://ec.test/",
	}
}

func TestMetaSettingsBlock(t *testing.T) {
	t.Parallel()
	owner := menuIdentity("org_admin", false, 1)
	cases := []struct {
		name   string
		status int
		body   string
		want   string // "" = nothing shown
	}{
		{"connected", 200, `{"enabled":true,"connection":{"status":"active"}}`, "Meta: подключено"},
		{"not connected", 200, `{"enabled":true,"connection":null}`, "Meta: не подключено"},
		{"revoked", 200, `{"enabled":true,"connection":{"status":"revoked"}}`, "Meta: не подключено"},
		{"error", 200, `{"enabled":true,"connection":{"status":"error"}}`, "Meta: ошибка подключения"},
		{"disabled", 200, `{"enabled":false,"connection":null}`, ""},
		{"forbidden", 403, `{"error":{"code":"forbidden"}}`, ""},
		{"api error", 500, `{"error":{"code":"internal"}}`, ""},
	}
	for _, c := range cases {
		b := metaTestBot(t, c.status, c.body)
		line, row := b.metaSettingsBlock(context.Background(), "ru", owner, "jwt")
		if line != c.want {
			t.Errorf("%s: line = %q, want %q", c.name, line, c.want)
		}
		if c.want == "" {
			if row != nil {
				t.Errorf("%s: row must be nil", c.name)
			}
			continue
		}
		want := "https://ec.test/o/" + owner.Current.OrgID.String() + "/ads/meta"
		if len(row) != 1 || row[0].URL != want {
			t.Errorf("%s: row = %+v, want url %s", c.name, row, want)
		}
	}
}

func TestMetaSettingsBlock_NotForManagerOrWithoutURL(t *testing.T) {
	t.Parallel()
	b := metaTestBot(t, 200, `{"enabled":true,"connection":null}`)
	if line, _ := b.metaSettingsBlock(context.Background(), "ru", menuIdentity("organizer", false, 1), "jwt"); line != "" {
		t.Errorf("manager got a line %q", line)
	}
	b.eventCenterURL = ""
	if line, _ := b.metaSettingsBlock(context.Background(), "ru", menuIdentity("org_admin", false, 1), "jwt"); line != "" {
		t.Errorf("no URL, got a line %q", line)
	}
}
