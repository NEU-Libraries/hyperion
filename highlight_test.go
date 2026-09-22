package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// withColour turns colour on for one test; under `go test` lipgloss sees no
// terminal and would otherwise render every style as plain text.
func withColour(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

const sampleMODS = `<?xml version="1.0" encoding="UTF-8"?>
<!-- descMetadata for w1 -->
<mods:mods xmlns:mods="http://www.loc.gov/mods/v3" version="3.7">
	<mods:titleInfo usage='primary'>
		<mods:title>Boston Harbor &amp; Environs, 1775 &#x2014; a chart</mods:title>
	</mods:titleInfo>
	<mods:note><![CDATA[x < y & "z"]]></mods:note>
	<mods:identifier type="hdl" displayLabel="a > b">http://hdl.handle.net/2047/w1</mods:identifier>
	<mods:extension/>
</mods:mods>`

func TestHighlightersOnlyAddColour(t *testing.T) {
	withColour(t)
	for name, c := range map[string]struct {
		src string
		hl  func(string) string
	}{
		"mods":            {sampleMODS, highlightXML},
		"broken xml":      {"<a b=\"unterminated><!-- no end\n< 3 &nope <c", highlightXML},
		"stray lt":        {"1 < 2 and <3", highlightXML},
		"json":            {`{"a": [1, -2.5e3, true, false, null], "b\"c": "d\\\"e", "f": {}}`, highlightJSON},
		"truncated json":  {`{"a": "no end`, highlightJSON},
		"multi-line json": {"{\n  \"k\": \"v\"\n}", highlightJSON},
	} {
		out := c.hl(c.src)
		if out == c.src && name != "stray lt" {
			t.Errorf("%s: nothing was coloured", name)
		}
		if got := ansi.Strip(out); got != c.src {
			t.Errorf("%s: text changed\n got %q\nwant %q", name, got, c.src)
		}
	}
}

func TestHighlightXMLTokens(t *testing.T) {
	withColour(t)
	out := highlightXML(sampleMODS)
	for tok, style := range map[string]lipgloss.Style{
		"mods:titleInfo":               synElement,
		"usage":                        synAttr,
		`"hdl"`:                        synString,
		`"a > b"`:                      synString,
		"&amp;":                        synLiteral,
		"&#x2014;":                     synLiteral,
		"<!-- descMetadata for w1 -->": synComment,
		`<![CDATA[x < y & "z"]]>`:      synDecl,
	} {
		if !strings.Contains(out, paint(style, tok)) {
			t.Errorf("%q not coloured as expected", tok)
		}
	}
	if strings.Contains(out, paint(synElement, "Boston")) {
		t.Error("character data should stay plain")
	}
}

func TestHighlightJSONKeysVersusValues(t *testing.T) {
	withColour(t)
	out := highlightJSON(`{"status": "ok"}`)
	if !strings.Contains(out, paint(synElement, `"status"`)) || !strings.Contains(out, paint(synString, `"ok"`)) {
		t.Errorf("key and value should differ: %q", out)
	}
}

func TestPaintKeepsMultiLineTokensUnpadded(t *testing.T) {
	withColour(t)
	src := "<!-- a\n\tlonger second line\nx -->"
	if got := ansi.Strip(paint(synComment, src)); got != src {
		t.Errorf("paint changed the text: %q", got)
	}
}

func TestResponseRenderPicksFormat(t *testing.T) {
	if got := ansi.Strip(responseRender([]byte(`{"error":"x"}`))(0)); got != "{\n  \"error\": \"x\"\n}" {
		t.Errorf("json response = %q", got)
	}
	if got := responseRender([]byte("<html>bad gateway</html>"))(0); got != "<html>bad gateway</html>" {
		t.Errorf("html response should stay plain: %q", got)
	}
}

func TestPagerCutsTabbedLinesInsteadOfWrapping(t *testing.T) {
	sess := testSession()
	sess.width, sess.height = 40, 30
	line := "\t<mods:identifier type=\"hdl\" displayLabel=\"a > b\">http://hdl.handle.net/2047/w1</mods:identifier>"
	s := staticPager(sess, "MODS", "", xmlRender(line+"\n"+line))
	s, _ = sendPager(s, initResult[pagerMsg](t, s.Init()))
	var shown []string
	for _, l := range strings.Split(ansi.Strip(s.vp.View()), "\n") {
		if strings.TrimSpace(l) != "" {
			shown = append(shown, l)
		}
	}
	if len(shown) != 2 || !strings.HasPrefix(shown[1], "    <mods:identifier") {
		t.Errorf("two long lines should show as two cut lines, got %q", shown)
	}
}

func TestPagerPans(t *testing.T) {
	sess := testSession()
	sess.width, sess.height = 20, 30
	s := staticPager(sess, "MODS", "", textRender("0123456789abcdefghijklmnopqrstuvwxyz"))
	s, _ = sendPager(s, initResult[pagerMsg](t, s.Init()))
	first := func() string { return strings.TrimSpace(strings.Split(ansi.Strip(s.vp.View()), "\n")[0]) }
	if got := first(); got != "0123456789abcdefghi" {
		t.Fatalf("before panning: %q", got)
	}
	s, _ = sendPager(s, tea.KeyMsg{Type: tea.KeyRight})
	if got := first(); !strings.HasPrefix(got, "89abc") {
		t.Errorf("→ should pan %d columns, view starts %q", panStep, got)
	}
	s, _ = sendPager(s, tea.KeyMsg{Type: tea.KeyLeft})
	if got := first(); !strings.HasPrefix(got, "0123") {
		t.Errorf("← should pan back, view starts %q", got)
	}
}
