package main

import (
	"errors"
	"strings"
	"testing"

	"hyperion/internal/atlas"
	"hyperion/internal/export"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// These drive the screens' Update directly with the messages their commands
// would produce (KeyMsg, authMsg, resolvedMsg, progressMsg, downloadDoneMsg),
// so the state machines and views are exercised without a terminal or network.
// Navigation commands (push/replace/pop) are pure, so tests may run them.

var (
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	esc   = tea.KeyMsg{Type: tea.KeyEsc}
)

func typed(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// testSession is already authenticated, so flow tests skip the network-backed
// auth command.
func testSession() *session {
	return &session{
		url: "http://example", token: "tok", concurrency: 4,
		client: atlas.New("http://example", "tok"),
		user:   atlas.User{Name: "Doe, Jane", NUID: "000000002"},
	}
}

func testApp() app {
	sess := testSession()
	return app{sess: sess, stack: []screen{newMenuScreen(sess)}}
}

// pidPrompt opens "Inspect an object by PID", second on the menu after
// Search.
func pidPrompt(t *testing.T, a app) app {
	t.Helper()
	a, _ = sendApp(a, tea.KeyMsg{Type: tea.KeyDown})
	return navigate(t, a, enter)
}

func sendApp(a app, msg tea.Msg) (app, tea.Cmd) {
	next, cmd := a.Update(msg)
	return next.(app), cmd
}

// navigate sends msg, then feeds the navigation message its command produces
// back into the app — what the Bubble Tea runtime would do.
func navigate(t *testing.T, a app, msg tea.Msg) app {
	t.Helper()
	a, cmd := sendApp(a, msg)
	if cmd == nil {
		t.Fatalf("%v produced no command", msg)
	}
	a, _ = sendApp(a, cmd())
	return a
}

func sendExport(s exportScreen, msg tea.Msg) (exportScreen, tea.Cmd) {
	next, cmd := s.Update(msg)
	return next.(exportScreen), cmd
}

func isPop(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(popMsg)
	return ok
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestCredentialPromptFlow(t *testing.T) {
	// No creds → prompt for URL (prefilled default), then token, then auth.
	sess := &session{concurrency: 4}
	s := newLoginScreen(sess)
	if s.state != loginURL {
		t.Fatalf("no creds: state = %v, want loginURL", s.state)
	}
	if s.urlInput.Value() != defaultURL {
		t.Errorf("URL not prefilled: %q", s.urlInput.Value())
	}
	next, _ := s.Update(enter) // accept default URL
	s = next.(loginScreen)
	if s.state != loginToken {
		t.Fatalf("after URL: state = %v, want loginToken", s.state)
	}
	next, _ = s.Update(typed("jwt.abc.123"))
	next, _ = next.Update(enter)
	s = next.(loginScreen)
	if s.state != loginAuth || sess.token != "jwt.abc.123" {
		t.Fatalf("after token: state = %v token = %q", s.state, sess.token)
	}

	// Stand in for the auth command's result: success replaces login with the menu.
	_, cmd := s.Update(authMsg{user: atlas.User{Name: "Doe, Jane", NUID: "000000002"}, client: atlas.New("u", "t")})
	r, ok := cmd().(replaceMsg)
	if !ok {
		t.Fatalf("auth ok: command gave %T, want replaceMsg", cmd())
	}
	if _, ok := r.s.(menuScreen); !ok {
		t.Errorf("auth ok: replaced with %T, want menuScreen", r.s)
	}
	if sess.user.NUID != "000000002" || sess.client == nil {
		t.Errorf("auth ok: session not populated: %+v", sess)
	}

	// With both creds present, the prompts are skipped entirely.
	if st := newLoginScreen(&session{url: "u", token: "t"}).state; st != loginAuth {
		t.Errorf("creds present: initial state = %v, want loginAuth", st)
	}
	// A rejected token surfaces as an error, not a silent pass.
	bad, _ := newLoginScreen(&session{url: "u", token: "t"}).Update(authMsg{err: atlas.ErrUnauthorized})
	if bad.(loginScreen).state != loginError {
		t.Errorf("auth fail: state = %v, want loginError", bad.(loginScreen).state)
	}
}

func TestAppNavigation(t *testing.T) {
	a := testApp()
	if v := a.View(); !strings.Contains(v, "HYPERION") || !strings.Contains(v, "Doe, Jane") {
		t.Errorf("header missing name/title:\n%s", v)
	}

	a, _ = sendApp(a, tea.KeyMsg{Type: tea.KeyDown})
	a, _ = sendApp(a, tea.KeyMsg{Type: tea.KeyDown})
	a = navigate(t, a, enter) // open export, third on the menu
	if _, ok := a.top().(exportScreen); !ok || len(a.stack) != 2 {
		t.Fatalf("after enter: top = %T depth = %d, want exportScreen at depth 2", a.top(), len(a.stack))
	}

	a = navigate(t, a, esc) // back out of export
	if _, ok := a.top().(menuScreen); !ok || len(a.stack) != 1 {
		t.Fatalf("after esc: top = %T depth = %d, want menuScreen at depth 1", a.top(), len(a.stack))
	}

	if _, cmd := sendApp(a, esc); cmd != nil {
		t.Error("esc on the menu should do nothing; esc means back everywhere else")
	}
	asked, cmd := sendApp(a, typed("q"))
	if cmd != nil || !asked.quitting {
		t.Error("q on the menu should ask before quitting")
	}
	if _, cmd := sendApp(asked, typed("y")); !isQuit(cmd) {
		t.Error("y should then quit")
	}
	if _, cmd := sendApp(a, popMsg{}); !isQuit(cmd) {
		t.Error("popping the last screen should quit")
	}
}

func TestPushDoesNotAliasEarlierStacks(t *testing.T) {
	a := testApp()
	sess := a.sess
	first, _ := sendApp(a, pushMsg{newExportScreen(sess)})
	second, _ := sendApp(a, pushMsg{newMenuScreen(sess)})
	if _, ok := first.top().(exportScreen); !ok {
		t.Errorf("an earlier app value was mutated: top = %T", first.top())
	}
	if _, ok := second.top().(menuScreen); !ok {
		t.Errorf("second push: top = %T", second.top())
	}
}

func TestFlowToConfirm(t *testing.T) {
	s := newExportScreen(testSession())
	if s.state != exportType {
		t.Fatalf("initial state = %v, want exportType", s.state)
	}

	s, _ = sendExport(s, enter) // choose Collection
	if s.state != exportPID {
		t.Fatalf("after enter, state = %v, want exportPID", s.state)
	}
	if !strings.Contains(s.View(), "PID:") {
		t.Errorf("PID prompt missing:\n%s", s.View())
	}

	s, _ = sendExport(s, typed("gqnkbq9"))
	s, _ = sendExport(s, enter)
	if s.state != exportOut {
		t.Fatalf("after PID, state = %v, want exportOut", s.state)
	}
	if got := s.outInput.Value(); got != "./gqnkbq9-mods" {
		t.Errorf("default out dir = %q, want ./gqnkbq9-mods", got)
	}

	s, _ = sendExport(s, enter)
	if s.state != exportResolving {
		t.Fatalf("after out, state = %v, want exportResolving", s.state)
	}

	// Stand in for the resolve command's result.
	s, _ = sendExport(s, resolvedMsg{
		container: atlas.Container{Kind: atlas.Collection, Title: "Test Collection",
			Members: []atlas.Member{{NOID: "a"}, {NOID: "b"}, {NOID: "c"}}},
		kind: atlas.Collection, pid: "gqnkbq9",
	})
	if s.state != exportConfirm || s.total != 3 {
		t.Fatalf("after resolved: state=%v total=%d", s.state, s.total)
	}
	if v := s.View(); !strings.Contains(v, `Export 3 MODS files from "Test Collection" to ./gqnkbq9-mods?`) {
		t.Errorf("confirm view:\n%s", v)
	}
	if strings.Contains(s.View(), "replaced") {
		t.Error("nothing is in the folder, so nothing should be said to be replaced")
	}
	if s.crumb() != "Export MODS: Collection gqnkbq9" {
		t.Errorf("crumb = %q", s.crumb())
	}
	back, _ := sendExport(s, typed("n"))
	if back.state != exportOut || back.outInput.Value() != "./gqnkbq9-mods" || back.pidInput.Value() != "gqnkbq9" {
		t.Errorf("n should go back to the folder with everything kept: state=%v", back.state)
	}
}

func TestExportEscStepsBack(t *testing.T) {
	s := newExportScreen(testSession())
	s, _ = sendExport(s, enter)
	s, _ = sendExport(s, typed("gqnkbq9"))
	s, _ = sendExport(s, enter)

	s, _ = sendExport(s, esc)
	if s.state != exportPID || s.pidInput.Value() != "gqnkbq9" {
		t.Fatalf("esc from out: state = %v pid = %q, want exportPID with PID kept", s.state, s.pidInput.Value())
	}
	s, _ = sendExport(s, esc)
	if s.state != exportType {
		t.Fatalf("esc from PID: state = %v, want exportType", s.state)
	}
	if _, cmd := sendExport(s, esc); !isPop(cmd) {
		t.Error("esc from the type picker should return to the menu")
	}
}

func TestResolveErrors(t *testing.T) {
	base := newExportScreen(testSession())
	base.state = exportResolving

	notFound, _ := sendExport(base, resolvedMsg{err: atlas.ErrNotFound, kind: atlas.Collection, pid: "zzz"})
	if notFound.state != exportError || !strings.Contains(notFound.View(), "PID zzz") {
		t.Errorf("not-found: state=%v view=%q", notFound.state, notFound.View())
	}
	back, _ := sendExport(notFound, enter)
	if back.state != exportPID || back.pidInput.Value() != "" && back.pidInput.Value() != "zzz" {
		t.Errorf("enter on the error should go back to the PID prompt: state=%v", back.state)
	}
	if again, cmd := sendExport(notFound, typed("x")); cmd != nil || again.state != exportError {
		t.Error("a stray key on the error should do nothing")
	}

	empty, _ := sendExport(base, resolvedMsg{container: atlas.Container{Kind: atlas.Set}, kind: atlas.Set, pid: "x"})
	if empty.state != exportError || !strings.Contains(empty.View(), "no members your account can view") {
		t.Errorf("empty: state=%v view=%q", empty.state, empty.View())
	}
}

func TestProgressTally(t *testing.T) {
	s := newExportScreen(testSession())
	s.state = exportDownloading
	s.total = 3
	s.outDir = "./out"

	s, _ = sendExport(s, progressMsg(export.Progress{NOID: "a"}))
	s, _ = sendExport(s, progressMsg(export.Progress{NOID: "b", Err: errors.New("boom")}))
	if s.completed != 2 || len(s.failures) != 1 {
		t.Fatalf("completed=%d failures=%d, want 2/1", s.completed, len(s.failures))
	}

	s, _ = sendExport(s, downloadDoneMsg{})
	if s.state != exportDone {
		t.Fatalf("after done, state=%v want exportDone", s.state)
	}
	if v := s.View(); !strings.Contains(v, "Saved 2") || !strings.Contains(v, "1 could not") {
		t.Errorf("done view:\n%s", v)
	}
	if _, cmd := sendExport(s, enter); !isPop(cmd) {
		t.Error("a key on the summary should return to the menu")
	}
}

func TestInspectFlow(t *testing.T) {
	a := testApp()
	a = pidPrompt(t, a)
	if _, ok := a.top().(pidPromptScreen); !ok {
		t.Fatalf("after enter: top = %T, want pidPromptScreen", a.top())
	}
	a, _ = sendApp(a, typed("w1"))
	a = navigate(t, a, enter)
	obj, ok := a.top().(objectScreen)
	if !ok || obj.pid != "w1" || obj.state != loading {
		t.Fatalf("after PID: top = %T %+v, want loading objectScreen for w1", a.top(), a.top())
	}

	// A result meant for another screen (a stale load) is ignored.
	a, _ = sendApp(a, objectMsg{id: obj.id + 1000, err: atlas.ErrNotFound})
	if a.top().(objectScreen).state != loading {
		t.Fatal("a result for another screen changed this one")
	}

	a.sess.width = 80
	a, _ = sendApp(a, objectMsg{id: obj.id, obj: atlas.Object{
		Type: atlas.TypeWork, NOID: "w1", Title: "Boston Harbor, 1775",
		Ancestors: []atlas.Ancestor{{NOID: "c0", Klass: "Community", Title: "Library"}, {NOID: "k1", Klass: "Collection"}},
		Fields: []atlas.Field{
			{Key: "id", Value: []byte(`"w1"`)},
			{Key: "title", Value: []byte(`"Boston Harbor, 1775"`)},
			{Key: "tombstoned", Value: []byte(`true`)},
			{Key: "depositor", Value: []byte(`"001234567"`)},
			{Key: "handle", Value: []byte(`null`)},
			{Key: "derivative_permissions", Value: []byte(`{"large": ["public"]}`)},
		},
	}})
	v := a.View()
	for _, want := range []string{
		"Work", "PID", "Boston Harbor, 1775",
		"Library (c0) › (untitled collection) (k1)",
		"Depositor (NUID)", "001234567", "Tombstoned", "yes", `{"large":["public"]}`,
		"› Record", "MODS history", "FileSets", "Associations", "Ancestors", "Permissions",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("object view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(strings.ToUpper(v), "NOID") {
		t.Errorf("object view says NOID; the UX term is PID:\n%s", v)
	}
	if strings.Count(v, "Boston Harbor, 1775") != 1 {
		t.Errorf("title should appear once (header only):\n%s", v)
	}

	a = navigate(t, a, enter) // the first section is the full record
	if pg, ok := a.top().(pagerScreen); !ok || pg.title != "Record" {
		t.Fatalf("enter on the overview: top = %T, want the Record pager", a.top())
	}
	a = navigate(t, a, esc)

	a = navigate(t, a, esc) // back to the prompt, PID kept
	p, ok := a.top().(pidPromptScreen)
	if !ok || p.input.Value() != "w1" {
		t.Fatalf("after esc: top = %T, want pidPromptScreen keeping w1", a.top())
	}
}

func TestInspectErrorMessages(t *testing.T) {
	for err, want := range map[error]string{
		atlas.ErrNotFound:     "No object has this PID",
		atlas.ErrForbidden:    "The object exists, but your account doesn't have permission to view it",
		atlas.ErrUnauthorized: "Atlas rejected the access token",
	} {
		s := newObjectScreen(testSession(), "zzz")
		next, _ := s.Update(objectMsg{id: s.id, err: err})
		got := next.(objectScreen)
		if got.state != failed || !strings.Contains(got.View(), want) {
			t.Errorf("%v: view = %q, want %q", err, got.View(), want)
		}
		if _, cmd := got.Update(enter); !isPop(cmd) {
			t.Errorf("%v: a key on the error should go back", err)
		}
	}
}

func TestFieldValues(t *testing.T) {
	for _, c := range []struct {
		key, raw string
		width    int
		want     string
	}{
		{"size", `2048`, 0, "2.0 KiB (2048 bytes)"},
		{"size", `12`, 0, "12 bytes"},
		{"position", `3`, 0, "3"},
		{"in_progress", `false`, 0, "no"},
		{"read_groups", `["public","staff"]`, 0, "public, staff"},
		{"description", `"line one\n  line two"`, 0, "line one line two"},
		{"description", `"abcdefghijklmnop"`, 10, "abcdefghi…"},
		{"description", `"abcdefghij"`, 10, "abcdefghij"},
	} {
		if got := fieldValue(c.key, []byte(c.raw), c.width); got != c.want {
			t.Errorf("fieldValue(%s, %s) = %q, want %q", c.key, c.raw, got, c.want)
		}
	}
	if got := fieldLabel("original_filename"); got != "Original filename" {
		t.Errorf("fieldLabel = %q", got)
	}
}

func TestTheMenuHasNoGapForAnEmptyTrail(t *testing.T) {
	lines := strings.Split(ansi.Strip(testApp().View()), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[1], "✓ Signed in") || lines[2] != "" || !strings.HasPrefix(lines[3], "╭─ What would you like to do?") {
		t.Errorf("the menu should sit one blank line under the signed-in line:\n%s", strings.Join(lines, "\n"))
	}
}
