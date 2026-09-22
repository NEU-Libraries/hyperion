package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const digest = "sha512:c6bb3b262e4b53f52daa6fdc4d4341503d69c2c6643d8224523b67e2de1f083990cf6a187fc6033ded492335c6e2fb29e13f9732bd6cef2dd1da5753057bb8cc"

// appAt is an app of the given size with the screen make builds (from the
// app's own session) pushed and loaded on top.
func appAt(t *testing.T, width, height int, make func(*session) screen) app {
	t.Helper()
	a := testApp()
	a.sess.width, a.sess.height = width, height
	s := make(a.sess)
	a, _ = sendApp(a, pushMsg{s})
	switch s.(type) {
	case pagerScreen:
		a, _ = sendApp(a, initResult[pagerMsg](t, a.top().Init()))
	case listScreen:
		a, _ = sendApp(a, initResult[listMsg](t, a.top().Init()))
	}
	return a
}

func visibleLines(t *testing.T, a app) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(a.View()), "\n")
	for i, l := range lines {
		if w := lipgloss.Width(l); w > a.sess.width-1 {
			t.Errorf("line %d is %d wide on a %d-column terminal: %q", i, w, a.sess.width, l)
		}
	}
	return lines
}

func TestWrappedRecordValuesKeepEveryCharacter(t *testing.T) {
	fields := []atlas.Field{{Key: "digest", Value: []byte(`"` + digest + `"`)}}
	for _, width := range []int{80, 110} {
		a := appAt(t, width, 40, func(sess *session) screen {
			return staticPager(sess, "Revision 1", "", fieldsRender(fields))
		})
		joined := strings.Join(strings.Fields(strings.Join(visibleLines(t, a), " ")), "")
		if !strings.Contains(joined, digest) {
			t.Errorf("at %d columns the wrapped digest lost characters:\n%s", width, ansi.Strip(a.View()))
		}
	}
}

func TestCutValuesShowTheirEllipsis(t *testing.T) {
	sess := testSession()
	a := testApp()
	a.sess = sess
	sess.width, sess.height = 60, 40
	s := newObjectScreen(sess, "w1")
	next, _ := s.Update(objectMsg{id: s.id, obj: atlas.Object{Type: atlas.TypeWork, NOID: "w1", Title: "Harbor",
		Fields: []atlas.Field{{Key: "description", Value: []byte(`"` + strings.Repeat("word ", 40) + `"`)}}}})
	a.stack = []screen{next}
	var found bool
	for _, l := range visibleLines(t, a) {
		if strings.HasPrefix(l, "Description") {
			found = strings.HasSuffix(strings.TrimRight(l, " "), "…")
		}
	}
	if !found {
		t.Errorf("a cut description should end in …:\n%s", ansi.Strip(a.View()))
	}

	la := appAt(t, 60, 40, func(sess *session) screen {
		return staticList(sess, "Files", "", "", []listItem{{pid: "b1", title: "sample-audio.mp3", kind: "File",
			note: "Original file · audio/mpeg · 2.2 MiB (2344649 bytes) and a long tail of detail"}})
	})
	for _, l := range visibleLines(t, la) {
		if strings.Contains(l, "sample-audio.mp3") && !strings.HasSuffix(strings.TrimRight(l, " "), "…") {
			t.Errorf("a cut list row should end in …: %q", l)
		}
	}
}

func TestSignInErrorOffersARetry(t *testing.T) {
	// A rejected token goes back to a fresh token prompt, URL kept.
	sess := &session{url: "http://atlas.example", token: "old"}
	s := newLoginScreen(sess)
	next, _ := s.Update(authMsg{err: atlas.ErrUnauthorized})
	s = next.(loginScreen)
	if v := s.View(); !strings.Contains(v, "enter to try again · esc to quit") {
		t.Errorf("sign-in error footer:\n%s", v)
	}
	if _, cmd := s.Update(typed("x")); cmd != nil {
		t.Error("a stray key on the sign-in error should do nothing")
	}
	next, _ = s.Update(enter)
	s = next.(loginScreen)
	if s.state != loginToken || s.tokenInput.Value() != "" || sess.token != "" || sess.url != "http://atlas.example" {
		t.Errorf("retry after a rejected token: state=%v token=%q url=%q", s.state, s.tokenInput.Value(), sess.url)
	}

	// Anything else — say Atlas can't be reached — goes back to the URL, kept.
	s = newLoginScreen(&session{url: "http://typo.example", token: "tok"})
	next, _ = s.Update(authMsg{err: errors.New("dial tcp: no such host")})
	next, _ = next.Update(enter)
	s = next.(loginScreen)
	if s.state != loginURL || s.urlInput.Value() != "http://typo.example" {
		t.Errorf("retry after a network error: state=%v url=%q", s.state, s.urlInput.Value())
	}
	if _, cmd := s.Update(esc); !isQuit(cmd) {
		t.Error("esc on the URL prompt should still quit")
	}
}

func TestExportWarnsBeforeReplacingFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.xml", "b.xml", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("edited"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newExportScreen(testSession())
	s.outDir, s.state = dir, exportResolving
	s, _ = sendExport(s, resolvedMsg{kind: atlas.Set, pid: "s1", container: atlas.Container{Kind: atlas.Set, Title: "Reading list",
		Members: []atlas.Member{{NOID: "a"}, {NOID: "b"}, {NOID: "c"}}}})
	if want := dir + " already has 2 of these files; they will be replaced."; !strings.Contains(ansi.Strip(s.View()), want) {
		t.Errorf("confirm should warn %q:\n%s", want, ansi.Strip(s.View()))
	}
}

func TestExportSummaryShowsPartialFailure(t *testing.T) {
	s := newExportScreen(testSession())
	s.state, s.total, s.outDir, s.container.Title = exportDownloading, 3, "./out", "Maps"
	s, _ = sendExport(s, progressMsg{NOID: "a"})
	s, _ = sendExport(s, progressMsg{NOID: "b", Err: atlas.ErrForbidden})
	s, _ = sendExport(s, progressMsg{NOID: "c"})
	s, _ = sendExport(s, downloadDoneMsg{})
	v := ansi.Strip(s.View())
	if !strings.Contains(v, `⚠ Saved 2 of 3 MODS files from "Maps"`) || !strings.Contains(v, "b: not readable") {
		t.Errorf("partial export summary:\n%s", v)
	}
}

// workOverview is an app showing a loaded Work with every section, at the
// given terminal size.
func workOverview(t *testing.T, width, height int, role string) app {
	t.Helper()
	a := testApp()
	a.sess.width, a.sess.height = width, height
	a.sess.user.Role = role
	a = navigate(t, a, enter)
	a, _ = sendApp(a, typed("w1"))
	a = navigate(t, a, enter)
	obj := a.top().(objectScreen)
	a, _ = sendApp(a, objectMsg{id: obj.id, obj: atlas.Object{Type: atlas.TypeWork, NOID: "w1", Title: "Harbor",
		Ancestors: []atlas.Ancestor{{NOID: "c0", Klass: "Community", Title: "Library"}},
		Fields: []atlas.Field{
			{Key: "id", Value: []byte(`"w1"`)},
			{Key: "valkyrie_id", Value: []byte(`"9fb283e3"`)},
			{Key: "thumbnail", Value: []byte(`"http://iiif/t.jpg"`)},
			{Key: "description", Value: []byte(`"A chart."`)},
			{Key: "depositor", Value: []byte(`"000000099"`)},
		}}})
	return a
}

func TestOverviewGroupsTheMenuAndLeadsThePreviewWithWhatMatters(t *testing.T) {
	a := workOverview(t, 110, 60, "admin")
	v := ansi.Strip(a.View())
	for group, first := range map[string]string{"Metadata": "MODS", "Content": "FileSets",
		"Relationships": "Linked collections", "Access": "Permissions"} {
		found := false
		for _, l := range strings.Split(v, "\n") {
			if f := strings.Fields(l); len(f) > 1 && f[0] == group {
				found = strings.Join(f[1:], " ") == first
			}
		}
		if !found {
			t.Errorf("the %s group should head %q:\n%s", group, first, v)
		}
	}
	if !strings.Contains(v, "Record preview") {
		t.Errorf("the preview should have a heading:\n%s", v)
	}
	if strings.Contains(v, "full admins only") {
		t.Error("a full admin's menu shouldn't mark MODS history as limited")
	}
	desc, valk := strings.Index(v, "Description"), strings.Index(v, "Valkyrie ID")
	if desc < 0 || valk < 0 || valk < desc {
		t.Errorf("technical fields should come after the descriptive ones:\n%s", v)
	}
	if v := ansi.Strip(workOverview(t, 110, 60, "privileged").View()); !strings.Contains(v, "MODS history (full admins only)") {
		t.Errorf("a delegated admin's menu should mark MODS history:\n%s", v)
	}
}

func TestShortTerminalKeepsTheHeaderAndScrollsTheMenu(t *testing.T) {
	a := workOverview(t, 110, 18, "admin")
	lines := visibleLines(t, a)
	if len(lines) > 18 {
		t.Errorf("the overview is %d lines on an 18-line terminal", len(lines))
	}
	v := strings.Join(lines, "\n")
	for _, want := range []string{"HYPERION", "Inspect › Work w1", "› Record", "↓ more"} {
		if !strings.Contains(v, want) {
			t.Errorf("short overview missing %q:\n%s", want, v)
		}
	}
	for range 9 { // down to Permissions, the last section
		a, _ = sendApp(a, tea.KeyMsg{Type: tea.KeyDown})
	}
	v = ansi.Strip(a.View())
	if !strings.Contains(v, "› Permissions") || !strings.Contains(v, "↑ more") || !strings.Contains(v, "HYPERION") {
		t.Errorf("after scrolling the menu:\n%s", v)
	}
}

func TestRecordValuesReadAsWords(t *testing.T) {
	prev := time.Local
	time.Local = time.FixedZone("EDT", -4*60*60)
	t.Cleanup(func() { time.Local = prev })

	for _, c := range []struct{ key, raw, want string }{
		{"ancestors", `[{"noid":"c0","klass":"Community","title":"Library"},{"noid":"k1","klass":"Collection","title":"Maps"}]`,
			"Library (c0) › Maps (k1)"},
		{"ancestors", `[]`, "none"},
		{"file_identifiers", `[{"id":"ocfl://a/v1/x.mp3"},{"id":"ocfl://a/v2/x.mp3"}]`, "ocfl://a/v1/x.mp3, ocfl://a/v2/x.mp3"},
		{"tombstoned_at", `"2026-09-15T20:14:55+00:00"`, "15 Sep 2026, 4:14 pm EDT"},
		{"created_at", `"2026-09-22T21:17:49Z"`, "22 Sep 2026, 5:17 pm EDT"},
		{"description", `"2026 was a year"`, "2026 was a year"},
	} {
		if got := ansi.Strip(fieldValue(c.key, []byte(c.raw), 0)); got != c.want {
			t.Errorf("fieldValue(%s, %s) = %q, want %q", c.key, c.raw, got, c.want)
		}
	}
	for key, want := range map[string]string{"type": "Classification", "personal_root": "Is a personal root",
		"created_at": "Created", "created": "Created"} {
		if got := fieldLabel(key); got != want {
			t.Errorf("fieldLabel(%s) = %q, want %q", key, got, want)
		}
	}
	if humanRole("service_file") != "Service file" || when("not a time") != "not a time" {
		t.Error("humanRole / when")
	}
}

func TestPermissionsDropTheRepeatedType(t *testing.T) {
	sess, _ := sessionAgainst(t, map[string]string{
		"/resources/w1/permissions": `{"resource":{"read":["public"],"type":"Work"}}`,
	})
	p := loadedPager(t, permissionsSection(sess, atlas.Object{Type: atlas.TypeWork, NOID: "w1"}).(pagerScreen))
	if v := ansi.Strip(p.View()); strings.Contains(v, "Classification") || strings.Contains(v, "Type") || !strings.Contains(v, "public") {
		t.Errorf("permissions:\n%s", v)
	}
}

func TestMissingDocumentsAreNoticesNotFailures(t *testing.T) {
	sess, _ := sessionAgainst(t, map[string]string{}) // every route 404s
	p := loadedPager(t, metsSection(sess, atlas.Object{Type: atlas.TypeFileSet, NOID: "fs1"}).(pagerScreen))
	v := ansi.Strip(p.View())
	if p.state != loaded || strings.Contains(v, "✗") || !strings.Contains(v, "This FileSet has no METS.") {
		t.Errorf("missing METS should read as a calm notice:\n%s", v)
	}

	s := newListScreen(sess, "MODS history", "", "No retained MODS versions.",
		func(context.Context, *atlas.Client) ([]listItem, error) { return nil, notice(modsHistoryLimit) })
	s, _ = sendList(s, initResult[listMsg](t, s.Init()))
	if v := ansi.Strip(s.View()); strings.Contains(v, "✗") || !strings.Contains(v, "only available to full admins") {
		t.Errorf("the MODS history limit should read as a notice:\n%s", v)
	}
}

func TestHeaderLabelsTheNUID(t *testing.T) {
	if v := testApp().View(); !strings.Contains(v, "Signed in as Doe, Jane (NUID 000000002)") {
		t.Errorf("header:\n%s", v)
	}
}

func TestPagerOffersPanAndWrapOnlyWhenALineIsCut(t *testing.T) {
	short := appAt(t, 80, 40, func(sess *session) screen { return staticPager(sess, "Permissions", "", textRender("Read  public")) })
	if v := ansi.Strip(short.View()); strings.Contains(v, "pan") || strings.Contains(v, "wrap") {
		t.Errorf("nothing is cut, so pan and wrap shouldn't be offered:\n%s", v)
	}
	long := appAt(t, 80, 40, func(sess *session) screen {
		return staticPager(sess, "MODS", "", textRender(strings.Repeat("x", 120)))
	})
	if v := ansi.Strip(long.View()); !strings.Contains(v, "←/→ pan · w wrap") {
		t.Errorf("a cut line should offer pan and wrap:\n%s", v)
	}
	// The footer follows the content, as on a list, rather than the screen's foot.
	lines := visibleLines(t, short)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 12 {
		t.Errorf("a one-line document's footer sits %d lines down:\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

func TestWrapLeavesNoOrphanWords(t *testing.T) {
	line := "    <mods:abstract>" + strings.Repeat("alpha beta gamma ", 6) + "the end</mods:abstract>"
	out, _ := wrapLines(line, 40)
	parts := strings.Split(out, "\n")
	for i, p := range parts[:len(parts)-1] {
		if len(strings.Fields(p)) < 2 {
			t.Errorf("line %d holds a single word: %q\n%s", i, p, out)
		}
	}
}

func TestRawViewsSayWhatTheyShow(t *testing.T) {
	sess := testSession()
	fields := []atlas.Field{{Key: "revision", Value: []byte("1")}}
	rev := staticPager(sess, "Revision 1", "", fieldsRender(fields)).
		withRaw(atlas.PathFileVersions("b1"), "b1-versions").rawTitled("Versions")
	rev, _ = sendPager(rev, initResult[pagerMsg](t, rev.Init()))
	_, cmd := sendPager(rev, typed("r"))
	raw := cmd().(pushMsg).s.(pagerScreen)
	if raw.title != "Versions (raw)" || raw.crumb() != "raw" {
		t.Errorf("revision's raw view: title=%q crumb=%q", raw.title, raw.crumb())
	}
}

func TestSavePromptStaysOneLine(t *testing.T) {
	var v saveState
	v.start("w1-mods", []byte("<mods/>"))
	v.notice, v.failed = "./x.xml already exists. Choose another name.", true
	if out := ansi.Strip(v.view()); strings.Contains(out, "\n") || !strings.Contains(out, "already exists") {
		t.Errorf("save prompt with a problem: %q", out)
	}
	v, _ = v.update(esc, nil)
	if v.active || v.notice != "" {
		t.Error("esc should close the prompt and clear its message")
	}
}
