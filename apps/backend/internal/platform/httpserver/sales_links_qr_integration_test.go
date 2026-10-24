//go:build integration

package httpserver

// sales_links_qr_integration_test.go — QL-5 (spec 43 section 4.2): the QR image
// of a sales link through the real router.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/qr"
)

func (f *sl4Fixture) getRaw(t *testing.T, path, token string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestSalesLinks_QRImage(t *testing.T) {
	f := newSL4Fixture(t)
	link := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster QR"}`, f.event))
	id, code, short := slStr(link, "id"), slStr(link, "code"), slStr(link, "short_url")
	if !strings.HasPrefix(short, slShortBase+"/") {
		t.Fatalf("short_url %q", short)
	}
	path := f.base() + "/" + id + "/qr"

	// PNG: exact size, headers, and the very bytes qr.PNG makes of short_url.
	resp, body := f.getRaw(t, path+"?size=512", f.ownerA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("png: %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=3600" {
		t.Errorf("cache-control %q", cc)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `inline; filename="`+code+`.png"` {
		t.Errorf("content-disposition %q", cd)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil || img.Bounds().Dx() != 512 || img.Bounds().Dy() != 512 {
		t.Fatalf("png decode: %v %v", err, img)
	}
	want, err := qr.PNG(short, 512, 4)
	if err != nil || !bytes.Equal(want, body) {
		t.Errorf("the PNG is not the QR of short_url %q (render error %v)", short, err)
	}

	// Defaults: 1024 px.
	_, def := f.getRaw(t, path, f.ownerA, nil)
	if img, err := png.Decode(bytes.NewReader(def)); err != nil || img.Bounds().Dx() != 1024 {
		t.Errorf("default size: %v", err)
	}

	// ETag round trip.
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	r304, b304 := f.getRaw(t, path+"?size=512", f.ownerA, map[string]string{"If-None-Match": etag})
	if r304.StatusCode != http.StatusNotModified || len(b304) != 0 {
		t.Errorf("If-None-Match: %d, %d bytes", r304.StatusCode, len(b304))
	}
	if r, _ := f.getRaw(t, path+"?size=512", f.ownerA, map[string]string{"If-None-Match": `"other"`}); r.StatusCode != http.StatusOK {
		t.Errorf("a different ETag must get the image, got %d", r.StatusCode)
	}

	// SVG.
	rs, sv := f.getRaw(t, path+"?format=svg&margin=2", f.ownerA, nil)
	if rs.StatusCode != http.StatusOK || rs.Header.Get("Content-Type") != "image/svg+xml; charset=utf-8" {
		t.Fatalf("svg: %d %q", rs.StatusCode, rs.Header.Get("Content-Type"))
	}
	if cd := rs.Header.Get("Content-Disposition"); cd != `inline; filename="`+code+`.svg"` {
		t.Errorf("svg content-disposition %q", cd)
	}
	var doc struct {
		ViewBox string `xml:"viewBox,attr"`
	}
	if err := xml.Unmarshal(sv, &doc); err != nil || doc.ViewBox == "" {
		t.Errorf("svg parse: %v viewBox %q", err, doc.ViewBox)
	}
	wantSVG, _ := qr.SVG(short, 2)
	if !bytes.Equal(wantSVG, sv) {
		t.Error("the SVG is not the QR of short_url")
	}

	// Bad parameters.
	for _, q := range []string{"?margin=1", "?margin=9", "?size=100", "?size=5000", "?format=gif", "?size=x"} {
		st, out := f.do(t, http.MethodGet, path+q, f.ownerA, "")
		if st != http.StatusBadRequest || slErrCode(out) != "sales_link.invalid_qr_params" {
			t.Errorf("%s: %d %q, want 400 sales_link.invalid_qr_params", q, st, slErrCode(out))
		}
	}

	// A manager reads (sales_link.read), another organization's owner does not.
	if r, _ := f.getRaw(t, path+"?size=256", f.managerA, nil); r.StatusCode != http.StatusOK {
		t.Errorf("manager: %d", r.StatusCode)
	}
	foreign := "/v1/organizations/" + f.orgB.String() + "/sales-links/" + id + "/qr"
	if st, out := f.do(t, http.MethodGet, foreign, f.ownerB, ""); st != http.StatusNotFound || slErrCode(out) != "sales_link.not_found" {
		t.Errorf("foreign org: %d %q, want 404 sales_link.not_found", st, slErrCode(out))
	}
	if st, _ := f.do(t, http.MethodGet, path, f.ownerB, ""); st != http.StatusForbidden {
		t.Errorf("org B on A's path: %d, want 403", st)
	}
	if st, out := f.do(t, http.MethodGet, f.base()+"/"+id[:8]+"/qr", f.ownerA, ""); st != http.StatusNotFound {
		t.Errorf("malformed id: %d %v", st, out)
	}
}
