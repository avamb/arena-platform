package eventmaterials

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

const migrationFile = "0153_event_materials.sql"

// upSQL returns the Up section of migration 0153 from the embedded FS.
func upSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile(migrations.Dir + "/" + migrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", migrationFile, err)
	}
	s := string(raw)
	if i := strings.Index(s, "-- +goose Down"); i >= 0 {
		s = s[:i]
	}
	return s
}

// tableSQL returns the CREATE TABLE statement of one table.
func tableSQL(t *testing.T, up, table string) string {
	t.Helper()
	start := strings.Index(up, "CREATE TABLE "+table+" (")
	if start < 0 {
		t.Fatalf("%s does not create %s", migrationFile, table)
	}
	end := strings.Index(up[start:], "\n);")
	if end < 0 {
		t.Fatalf("cannot find the end of %s", table)
	}
	return up[start : start+end]
}

func TestLimitsMatchMigration0153(t *testing.T) {
	up := upSQL(t)
	cases := []struct {
		table string
		max   int
	}{
		{"event_gallery_items", MaxGalleryPhotos},
		{"event_videos", MaxVideos},
		{"event_partner_links", MaxPartnerLinks},
		{"event_report_photos", MaxReportPhotos},
	}
	for _, c := range cases {
		ddl := tableSQL(t, up, c.table)
		want := fmt.Sprintf("CHECK (position BETWEEN 0 AND %d)", c.max-1)
		if !strings.Contains(ddl, want) {
			t.Errorf("%s: migration lacks %q; limit constant and schema drifted", c.table, want)
		}
		if !strings.Contains(ddl, "UNIQUE (event_id, position)") {
			t.Errorf("%s: (event_id, position) is not UNIQUE, the position CHECK would not cap rows", c.table)
		}
	}
}

func TestTextLimitsMatchMigration0153(t *testing.T) {
	up := upSQL(t)
	for table, want := range map[string][]string{
		"event_gallery_items": {fmt.Sprintf("char_length(caption) BETWEEN 1 AND %d", MaxCaptionRunes)},
		"event_report_photos": {fmt.Sprintf("char_length(caption) BETWEEN 1 AND %d", MaxCaptionRunes)},
		"event_videos": {
			fmt.Sprintf("char_length(url) <= %d", MaxURLLength),
			fmt.Sprintf("char_length(video_id) BETWEEN 1 AND %d", MaxVideoIDLength),
		},
		"event_partner_links": {
			fmt.Sprintf("char_length(url) <= %d", MaxURLLength),
			fmt.Sprintf("char_length(btrim(seller_name)) BETWEEN 1 AND %d", MaxSellerNameRunes),
		},
		"event_map_points": {fmt.Sprintf("char_length(label) BETWEEN 1 AND %d", MaxMapLabelRunes)},
	} {
		ddl := tableSQL(t, up, table)
		for _, w := range want {
			if !strings.Contains(ddl, w) {
				t.Errorf("%s: migration lacks %q", table, w)
			}
		}
	}
}

// listIn extracts the quoted values of the first "<column> IN (...)" CHECK.
func listIn(t *testing.T, ddl, column string) []string {
	t.Helper()
	re := regexp.MustCompile(column + `\s+IN\s*\(([^)]*)\)`)
	m := re.FindStringSubmatch(ddl)
	if m == nil {
		t.Fatalf("no %s IN (...) check", column)
	}
	var out []string
	for _, part := range strings.Split(m[1], ",") {
		out = append(out, strings.Trim(strings.TrimSpace(part), "'"))
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

func TestAllowlistsMatchMigration0153(t *testing.T) {
	up := upSQL(t)
	if got := listIn(t, tableSQL(t, up, "event_videos"), "provider"); !sameSet(got, VideoProviders) {
		t.Errorf("event_videos.provider CHECK %v != VideoProviders %v", got, VideoProviders)
	}
	if got := listIn(t, tableSQL(t, up, "event_page_stats"), "kind"); !sameSet(got, StatKinds) {
		t.Errorf("event_page_stats.kind CHECK %v != StatKinds %v", got, StatKinds)
	}
}

func TestOwnerTypesInMediaCheck(t *testing.T) {
	up := upSQL(t)
	for _, ot := range []string{OwnerTypeGallery, OwnerTypeReport} {
		if !strings.Contains(up, "'"+ot+"'") {
			t.Errorf("migration does not allow owner type %q", ot)
		}
	}
}
