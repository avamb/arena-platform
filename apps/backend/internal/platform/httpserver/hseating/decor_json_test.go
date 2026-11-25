package hseating

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const decorWithCode = `<rect width="5" onload="x()"/><script>x()</script><use href="https://example.com/a.svg#b"/>`
const decorDrawingOnly = `<rect width="5"/><use/>`

func TestSanitizedGeometryJSON_KeepsDrawingOnlyBytes(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"canvas": {"width": 1}, "decor_svg": "<rect width=\"5\"/>"}`)
	if got := sanitizedGeometryJSON(raw); string(got) != string(raw) {
		t.Fatalf("sanitizedGeometryJSON changed drawing-only geometry: %s", got)
	}
	for _, in := range []string{``, `null`, `[]`, `{"canvas":{}}`, `{"decor_svg":5}`} {
		if got := sanitizedGeometryJSON(json.RawMessage(in)); string(got) != in {
			t.Fatalf("sanitizedGeometryJSON(%q) = %q", in, got)
		}
	}
}

func TestSanitizedGeometryJSON_DropsCode(t *testing.T) {
	t.Parallel()
	in, _ := json.Marshal(map[string]any{"canvas": map[string]any{"width": 1}, "decor_svg": decorWithCode})
	var out struct {
		Canvas   map[string]any `json:"canvas"`
		DecorSVG string         `json:"decor_svg"`
	}
	if err := json.Unmarshal(sanitizedGeometryJSON(in), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.DecorSVG != decorDrawingOnly {
		t.Fatalf("decor_svg = %q, want %q", out.DecorSVG, decorDrawingOnly)
	}
	if out.Canvas["width"] != float64(1) {
		t.Fatalf("canvas lost: %v", out.Canvas)
	}
}

func TestSanitizeDecorCached(t *testing.T) {
	t.Parallel()
	for i := 0; i < 3; i++ {
		if got := sanitizeDecorCached(decorWithCode); got != decorDrawingOnly {
			t.Fatalf("round %d: %q, want %q", i, got, decorDrawingOnly)
		}
		if got := sanitizeDecorCached(decorDrawingOnly); got != decorDrawingOnly {
			t.Fatalf("round %d: drawing-only decor changed to %q", i, got)
		}
	}
	// The cache stays bounded however many distinct decors pass through.
	for i := 0; i < decorCacheMaxEntries+10; i++ {
		_ = sanitizeDecorCached(`<rect width="` + strconv.Itoa(i) + `" onclick="x()"/>`)
	}
	decorCache.mu.Lock()
	n, size := len(decorCache.entries), decorCache.bytes
	decorCache.mu.Unlock()
	if n > decorCacheMaxEntries || size > decorCacheMaxBytes {
		t.Fatalf("cache holds %d entries, %d bytes", n, size)
	}
}

func TestRenderers_ServeDrawingOnlyDecor(t *testing.T) {
	t.Parallel()
	g := mkGeometry()
	g.DecorSVG = decorWithCode
	vip, std := uuid.New(), uuid.New()
	bodies := map[string]string{
		"layout.svg": string(RenderBSSLayoutSVG(g, mkSessionSeats(vip, std), mkTiers(vip, std), 1)),
		"sbt10":      string(RenderSBT10SVG(g, mkSessionSeats(vip, std), mkTiers(vip, std), nil, 1)),
	}
	for name, body := range bodies {
		if !strings.Contains(body, `<g id="Decor">`+decorDrawingOnly) {
			t.Errorf("%s: no drawing-only decor in\n%s", name, body)
		}
		for _, bad := range []string{"<script", "onload", "example.com"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: body holds %q", name, bad)
			}
		}
	}
}

func TestSetPlanSVGHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	SetPlanSVGHeaders(h)
	want := map[string]string{
		"Content-Type":            "image/svg+xml; charset=utf-8",
		"Content-Security-Policy": "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; sandbox",
		"X-Content-Type-Options":  "nosniff",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
