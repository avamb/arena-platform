package eventtext

import (
	"strings"
	"testing"
)

func TestViewDescription_ArtRow(t *testing.T) {
	art := `<h3>Head</h3><p>Hi <strong>you</strong> <a href="https://example.com/a">site</a></p><ul><li>one</li><li>two</li></ul>`
	v := ViewDescription(art, "art")
	if want := `<h3>Head</h3><p>Hi <strong>you</strong> <a href="https://example.com/a" rel="noopener noreferrer" target="_blank">site</a></p><ul><li>one</li><li>two</li></ul>`; v.HTML != want {
		t.Errorf("HTML = %q, want %q", v.HTML, want)
	}
	if want := "Head\n\nHi you site (https://example.com/a)\n\n• one\n• two"; v.Text != want {
		t.Errorf("Text = %q, want %q", v.Text, want)
	}
	if want := "Head\n\nHi **you** site (https://example.com/a)\n\n- one\n- two"; v.Legacy != want {
		t.Errorf("Legacy = %q, want %q", v.Legacy, want)
	}
}

func TestViewDescription_TextRowKeepsItsStoredTextAsLegacy(t *testing.T) {
	old := "**Bold** and <b>tag</b>\n\n- one\n- two"
	v := ViewDescription(old, "text")
	if v.Legacy != old {
		t.Errorf("Legacy = %q, want the stored text", v.Legacy)
	}
	if want := "<p><strong>Bold</strong> and &lt;b&gt;tag&lt;/b&gt;</p><ul><li>one</li><li>two</li></ul>"; v.HTML != want {
		t.Errorf("HTML = %q, want %q", v.HTML, want)
	}
	if want := "Bold and <b>tag</b>\n\n• one\n• two"; v.Text != want {
		t.Errorf("Text = %q, want %q", v.Text, want)
	}
	// An unknown flag is the old text: the tags are never taken for markup.
	if u := ViewDescription("<p>x</p>", ""); !strings.Contains(u.HTML, "&lt;p&gt;") || u.Legacy != "<p>x</p>" {
		t.Errorf("unflagged = %+v", u)
	}
}

func TestViewDescription_HostileArtAndEmpty(t *testing.T) {
	v := ViewDescription(`<p onclick="x()">a<script>alert(1)</script><a href="javascript:alert(2)">b</a></p>`, "art")
	for _, bad := range []string{"<script", "onclick", "javascript:"} {
		if strings.Contains(v.HTML, bad) {
			t.Errorf("HTML %q contains %q", v.HTML, bad)
		}
	}
	if e := ViewDescription("", "art"); e.HTML != "" || e.Text != "" || e.Legacy != "" {
		t.Errorf("empty = %+v", e)
	}
}
