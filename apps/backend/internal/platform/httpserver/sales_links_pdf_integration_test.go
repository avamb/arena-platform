//go:build integration

package httpserver

// sales_links_pdf_integration_test.go — QL-13 (spec 43 section 6.5): the
// printable PDF insert of a sales link through the real router.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/pdfinsert"
)

// slPDFDrawnText joins every "Td (<utf-16be>)Tj" run of an uncompressed
// content stream (the pdfDrawnText approach of delivery/pdf's tests).
func slPDFDrawnText(doc []byte) string {
	var parts []string
	marker := []byte("Td (")
	for rest := doc; ; {
		i := bytes.Index(rest, marker)
		if i < 0 {
			break
		}
		rest = rest[i+len(marker):]
		var raw []byte
		n, ok := 0, false
		for j := 0; j < len(rest); {
			if rest[j] == '\\' && j+1 < len(rest) {
				c := rest[j+1]
				if c == 'r' {
					c = '\r'
				}
				raw = append(raw, c)
				j += 2
				continue
			}
			if bytes.HasPrefix(rest[j:], []byte(")Tj")) {
				n, ok = j+3, true
				break
			}
			raw = append(raw, rest[j])
			j++
		}
		if !ok {
			break
		}
		var sb strings.Builder
		for k := 0; k+1 < len(raw); k += 2 {
			sb.WriteRune(rune(binary.BigEndian.Uint16(raw[k:])))
		}
		parts = append(parts, sb.String())
		rest = rest[n:]
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

var slMediaBoxRe = regexp.MustCompile(`/MediaBox \[0 0 ([0-9.]+) ([0-9.]+)\]`)

func slMediaBox(t *testing.T, out []byte) (w, h float64) {
	t.Helper()
	m := slMediaBoxRe.FindSubmatch(out)
	if m == nil {
		t.Fatal("no MediaBox in the PDF")
	}
	w, _ = strconv.ParseFloat(string(m[1]), 64)
	h, _ = strconv.ParseFloat(string(m[2]), 64)
	return w, h
}

func TestSalesLinks_PDFInsert(t *testing.T) {
	f := newSL4Fixture(t)
	orgName := "SL4 A " + strings.TrimPrefix(f.orgASlug, "sl4-a-")

	// ── an event link: the event name, the English line, the address ──────
	ev := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster insert"}`, f.event))
	id, code, short := slStr(ev, "id"), slStr(ev, "code"), slStr(ev, "short_url")
	path := f.base() + "/" + id + "/qr"

	resp, body := f.getRaw(t, path+"?format=pdf", f.ownerA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pdf: %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("content type %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `inline; filename="`+code+`-a6.pdf"` {
		t.Errorf("content-disposition %q", cd)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=3600" {
		t.Errorf("cache-control %q", cc)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Fatal("the body is not a PDF")
	}
	if w, h := slMediaBox(t, body); w < 297 || w > 298 || h < 419 || h > 420 {
		t.Errorf("A6 page is %.2f x %.2f pt", w, h)
	}
	text := slPDFDrawnText(body)
	for _, want := range []string{"SL4 Show", orgName, "Scan and buy tickets", strings.TrimPrefix(short, "https://")} {
		if !strings.Contains(text, want) {
			t.Errorf("the insert lacks %q:\n%s", want, text)
		}
	}
	// The insert is byte-deterministic, so the ETag round trip works.
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if r304, b304 := f.getRaw(t, path+"?format=pdf", f.ownerA, map[string]string{"If-None-Match": etag}); r304.StatusCode != http.StatusNotModified || len(b304) != 0 {
		t.Errorf("If-None-Match: %d, %d bytes", r304.StatusCode, len(b304))
	}
	if _, again := f.getRaw(t, path+"?format=pdf", f.ownerA, nil); !bytes.Equal(again, body) {
		t.Error("two renders of the same insert differ")
	}

	// ── A5 ────────────────────────────────────────────────────────────────
	r5, b5 := f.getRaw(t, path+"?format=pdf&paper=a5", f.ownerA, nil)
	if r5.StatusCode != http.StatusOK {
		t.Fatalf("a5: %d %s", r5.StatusCode, b5)
	}
	if cd := r5.Header.Get("Content-Disposition"); cd != `inline; filename="`+code+`-a5.pdf"` {
		t.Errorf("a5 content-disposition %q", cd)
	}
	if w, h := slMediaBox(t, b5); w < 419 || w > 420 || h < 595 || h > 596 {
		t.Errorf("A5 page is %.2f x %.2f pt", w, h)
	}

	// ── a session link: the date on the venue's clock as the second line ──
	se := f.create(t, fmt.Sprintf(`{"target":{"kind":"session","session_id":%q},"label":"Date insert"}`, f.session))
	var start time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT start_at FROM sessions WHERE id = $1`, f.session).Scan(&start); err != nil {
		t.Fatal(err)
	}
	_, sb := f.getRaw(t, f.base()+"/"+slStr(se, "id")+"/qr?format=pdf", f.ownerA, nil)
	stext := slPDFDrawnText(sb)
	for _, want := range []string{"SL4 Show", pdfinsert.SessionLine(start, "Europe/Prague")} {
		if !strings.Contains(stext, want) {
			t.Errorf("the session insert lacks %q:\n%s", want, stext)
		}
	}

	// ── a promoter link and the organization's own page ───────────────────
	pr := f.create(t, fmt.Sprintf(`{"target":{"kind":"promoter_page","promoter_id":%q},"label":"Promoter insert"}`, f.promoter))
	if _, pb := f.getRaw(t, f.base()+"/"+slStr(pr, "id")+"/qr?format=pdf", f.ownerA, nil); !strings.Contains(slPDFDrawnText(pb), "SL4 Promoter") {
		t.Errorf("the promoter insert lacks the promoter name:\n%s", slPDFDrawnText(pb))
	}
	og := f.create(t, `{"target":{"kind":"org_page"},"label":"Card insert"}`)
	if _, ob := f.getRaw(t, f.base()+"/"+slStr(og, "id")+"/qr?format=pdf", f.ownerA, nil); strings.Count(slPDFDrawnText(ob), orgName) != 1 {
		t.Errorf("the organization insert must print the name once, not as title and header both:\n%s", slPDFDrawnText(ob))
	}

	// ── refusals and access ───────────────────────────────────────────────
	for _, q := range []string{"?format=pdf&paper=a4", "?format=pdf&paper=letter", "?format=pdf&size=10"} {
		st, out := f.do(t, http.MethodGet, path+q, f.ownerA, "")
		if st != http.StatusBadRequest || slErrCode(out) != "sales_link.invalid_qr_params" {
			t.Errorf("%s: %d %q, want 400 sales_link.invalid_qr_params", q, st, slErrCode(out))
		}
	}
	if r, _ := f.getRaw(t, path+"?format=pdf", f.managerA, nil); r.StatusCode != http.StatusOK {
		t.Errorf("manager: %d", r.StatusCode)
	}
	if r, _ := f.getRaw(t, path+"?format=pdf", f.agentA, nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("agent: %d, want 403", r.StatusCode)
	}
	foreign := "/v1/organizations/" + f.orgB.String() + "/sales-links/" + id + "/qr?format=pdf"
	if st, out := f.do(t, http.MethodGet, foreign, f.ownerB, ""); st != http.StatusNotFound || slErrCode(out) != "sales_link.not_found" {
		t.Errorf("foreign org: %d %q", st, slErrCode(out))
	}
}
