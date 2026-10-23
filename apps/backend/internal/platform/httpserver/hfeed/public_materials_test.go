package hfeed

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestMaterialsFromRow_Shapes(t *testing.T) {
	g1, g2, r1 := uuid.New(), uuid.New(), uuid.New()
	row := gen.EventPublicMaterialsRow{
		Gallery: []byte(`[{"media_id":"` + g1.String() + `","caption":"Hall","width":1200,"height":800},` +
			`{"media_id":"` + g2.String() + `","caption":null,"width":null,"height":null}]`),
		Videos: []byte(`[{"provider":"youtube","video_id":"dQw4w9WgXcQ","url":"https://youtu.be/dQw4w9WgXcQ"},` +
			`{"provider":"vimeo","video_id":"76979871","url":"https://vimeo.com/76979871"},` +
			`{"provider":"rutube","video_id":"0123456789abcdef0123456789abcdef","url":"https://rutube.ru/video/0123456789abcdef0123456789abcdef/"},` +
			`{"provider":"https","video_id":null,"url":"https://example.com/clip"}]`),
		PartnerLinks: []byte(`[{"url":"https://ticketportal.cz/e/1","seller_name":"Ticketportal"}]`),
		MapPoint:     []byte(`{"latitude":50.075538,"longitude":14.4378,"label":"Main hall"}`),
		ReportPhotos: []byte(`[{"media_id":"` + r1.String() + `","caption":null,"width":null,"height":null}]`),
	}
	m, err := (&Handler{}).materialsFromRow(context.Background(), row)
	if err != nil {
		t.Fatalf("materialsFromRow: %v", err)
	}

	if len(m.Gallery) != 2 || m.Gallery[0].MediaID != g1.String() || m.Gallery[1].MediaID != g2.String() {
		t.Fatalf("gallery order/ids = %+v", m.Gallery)
	}
	if m.Gallery[0].URL != "/v1/media-files/"+g1.String() {
		t.Errorf("unsigned handler keeps the host-relative URL, got %q", m.Gallery[0].URL)
	}
	if m.Gallery[0].Caption == nil || *m.Gallery[0].Caption != "Hall" || *m.Gallery[0].Width != 1200 || *m.Gallery[0].Height != 800 {
		t.Errorf("gallery[0] = %+v", m.Gallery[0])
	}
	if m.Gallery[1].Caption != nil || m.Gallery[1].Width != nil || m.Gallery[1].Height != nil {
		t.Errorf("gallery[1] must carry nulls: %+v", m.Gallery[1])
	}

	wantEmbed := []string{
		"https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
		"https://player.vimeo.com/video/76979871",
		"https://rutube.ru/play/embed/0123456789abcdef0123456789abcdef",
		"",
	}
	if len(m.Videos) != 4 {
		t.Fatalf("videos = %+v", m.Videos)
	}
	for i, want := range wantEmbed {
		got := ""
		if m.Videos[i].EmbedURL != nil {
			got = *m.Videos[i].EmbedURL
		}
		if got != want {
			t.Errorf("video %d embed_url = %q, want %q", i, got, want)
		}
	}
	if m.Videos[3].VideoID != nil || m.Videos[3].URL != "https://example.com/clip" {
		t.Errorf("plain https video = %+v", m.Videos[3])
	}

	if len(m.PartnerLinks) != 1 || m.PartnerLinks[0].SellerName != "Ticketportal" {
		t.Errorf("partner links = %+v", m.PartnerLinks)
	}
	if m.MapPoint == nil || m.MapPoint.Latitude != 50.075538 || m.MapPoint.Longitude != 14.4378 || *m.MapPoint.Label != "Main hall" {
		t.Errorf("map point = %+v", m.MapPoint)
	}
	if len(m.ReportPhotos) != 1 || m.ReportPhotos[0].MediaID != r1.String() {
		t.Errorf("report photos = %+v", m.ReportPhotos)
	}
}

func TestMaterialsFromRow_AnEmbedOfAMalformedIDFallsBackToThePlainLink(t *testing.T) {
	row := gen.EventPublicMaterialsRow{
		Gallery: []byte(`[]`), PartnerLinks: []byte(`[]`), ReportPhotos: []byte(`[]`),
		Videos: []byte(`[{"provider":"youtube","video_id":"../evil","url":"https://youtube.com/x"}]`),
	}
	m, err := (&Handler{}).materialsFromRow(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	if m.Videos[0].EmbedURL != nil {
		t.Errorf("a malformed id must not become an iframe address: %q", *m.Videos[0].EmbedURL)
	}
}

func TestPublicMaterials_EmptyIsEmptyListsAndNullPoint(t *testing.T) {
	raw, err := json.Marshal(emptyPublicMaterials())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"gallery":[],"videos":[],"partner_links":[],"map_point":null,"report_photos":[]}`
	if string(raw) != want {
		t.Errorf("empty materials = %s, want %s", raw, want)
	}

	// A row of empty arrays (what the query answers) decodes to the same thing.
	m, err := (&Handler{}).materialsFromRow(context.Background(), gen.EventPublicMaterialsRow{
		Gallery: []byte(`[]`), Videos: []byte(`[]`), PartnerLinks: []byte(`[]`), MapPoint: nil, ReportPhotos: []byte(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ = json.Marshal(m); string(raw) != want {
		t.Errorf("decoded empty row = %s, want %s", raw, want)
	}
}

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestHostedPageResponse_ExistingFieldsUnchanged is the snapshot of task 1-2c:
// the hosted page answer keeps every field it had; `materials` is the only
// addition. A fully populated response is marshalled so that the omitempty
// blocks (seller, promoter, contact_form) show up too.
func TestHostedPageResponse_ExistingFieldsUnchanged(t *testing.T) {
	full := hostedPageResponse{
		Seller:      &hostedPageSellerResponse{},
		Promoter:    &hostedPagePromoterResponse{},
		ContactForm: &hostedPageContactForm{},
	}
	got := strings.Join(keysOf(t, full), ",")
	want := "contact_form,default_locale,event,feed_token,materials,org,promoter,seller"
	if got != want {
		t.Errorf("hostedPageResponse keys = %s, want %s", got, want)
	}
	// What existed before 1-2c, as a subset check that survives future additions.
	for _, k := range []string{"org", "event", "feed_token", "default_locale", "seller", "promoter", "contact_form"} {
		if !strings.Contains(","+got+",", ","+k+",") {
			t.Errorf("existing top-level key %q vanished", k)
		}
	}

	if got, want := strings.Join(keysOf(t, hostedPageOrgResponse{}), ","), "logo_url,name,slug"; got != want {
		t.Errorf("org keys = %s, want %s", got, want)
	}
	const wantEvent = "age_rating,description,feed_token,first_session_at,first_session_doors_at,first_session_timezone," +
		"id,image_url,last_session_at,poster_url,session_count,short_description,slug,title,venue_names,venues"
	ev := hostedPageEventResponse{FeedToken: "x"}
	if got := strings.Join(keysOf(t, ev), ","); got != wantEvent {
		t.Errorf("event keys = %s, want %s", got, wantEvent)
	}
}

// TestPublicFeedEventResponse_ExistingFieldsUnchanged: the event of the widget
// feed keeps its fields (the detail adds sessions, since 1-2c materials, and venues).
func TestPublicFeedEventResponse_ExistingFieldsUnchanged(t *testing.T) {
	const want = "created_at,description,display_number,first_session_at,id,image_url,last_session_at,name,org_id," +
		"poster_media_id,poster_url,status,updated_at,venue_names,visibility"
	if got := strings.Join(keysOf(t, publicFeedEventResponse{}), ","); got != want {
		t.Errorf("feed event keys = %s, want %s", got, want)
	}
}
