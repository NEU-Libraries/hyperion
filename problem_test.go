package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"
)

const rails500 = `{"status":500,"error":"Internal Server Error",` +
	`"exception":"#<NoMethodError: undefined method 'plain_title' for an instance of Delegate>",` +
	`"traces":{"Application Trace":["app/controllers/resources_controller.rb:30:in 'show'"]}}`

// atlasAnswering serves every path with one status and body.
func atlasAnswering(t *testing.T, status int, body string) *atlas.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return atlas.New(srv.URL, "tok")
}

func TestExplainServerError(t *testing.T) {
	_, err := atlasAnswering(t, 500, rails500).Inspect(context.Background(), "w0vt6v0")
	p := inspectProblem("w0vt6v0", err)
	if p.headline != "Couldn't open PID w0vt6v0." || !strings.Contains(p.reason, "fault in Atlas, not in your token or the PID") {
		t.Errorf("headline/reason = %q / %q", p.headline, p.reason)
	}
	want := []string{
		"NoMethodError: undefined method 'plain_title' for an instance of Delegate",
		"GET /resources/w0vt6v0 → HTTP 500 Internal Server Error",
	}
	if strings.Join(p.details, "|") != strings.Join(want, "|") {
		t.Errorf("details = %q", p.details)
	}
	if string(p.raw) != rails500 {
		t.Error("the full response should be kept for the d key")
	}

	v := p.body(60)
	if strings.Contains(v, `"status":500`) {
		t.Errorf("the raw body leaked into the panel:\n%s", v)
	}
	for _, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 60 {
			t.Errorf("line wider than 60: %q", line)
		}
	}
	if k := p.keys(backKeys); k != "d to see Atlas's full response · "+backKeys {
		t.Errorf("keys should offer d: %q", k)
	}
}

func TestExplainOtherFailures(t *testing.T) {
	ctx := context.Background()
	_, unavailable := atlasAnswering(t, 503, "<html>down</html>").Inspect(ctx, "x")
	_, garbled := atlasAnswering(t, 200, "{not json").Whoami(ctx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now
	_, unreachable := atlas.New("http://"+addr, "tok").Whoami(ctx)

	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"503", unavailable, "Atlas is unavailable right now (HTTP 503)"},
		{"bad body", garbled, "Atlas sent an answer Hyperion couldn't read"},
		{"no server", unreachable, "Hyperion couldn't reach Atlas"},
		{"tombstoned", atlas.ErrGone, "tombstoned"},
		{"worded", userErr("MODS history is admin only."), "MODS history is admin only."},
	} {
		if p := explain("Couldn't load MODS.", c.err); !strings.Contains(p.reason, c.want) {
			t.Errorf("%s: reason = %q, want %q", c.name, p.reason, c.want)
		}
	}
	if p := explain("x", unavailable); p.raw == nil {
		t.Error("a 503 with a body should keep it for the d key")
	}
}

func TestProblemDetailsKey(t *testing.T) {
	sess := testSession()
	p := explain("Couldn't open PID w0vt6v0.", &atlas.HTTPError{Path: "/resources/w0vt6v0", Status: 500, Body: []byte(rails500)})

	d := problemKey(sess, p, typed("d"), pop)
	pg, ok := d().(pushMsg).s.(pagerScreen)
	if !ok || pg.title != "Atlas's response" {
		t.Fatalf("d should open the response pager, got %T", d())
	}
	pg, _ = sendPager(pg, initResult[pagerMsg](t, pg.Init()))
	if !strings.Contains(pg.View(), `"exception": "#<NoMethodError`) {
		t.Errorf("response pager should show the indented body:\n%s", pg.View())
	}
	if !isPop(problemKey(sess, p, enter, pop)) {
		t.Error("any other key should go back")
	}
	if cmd := problemKey(sess, problem{headline: "x"}, typed("d"), pop); cmd != nil {
		t.Error("d with no response to show should do nothing")
	}
	if cmd := problemKey(sess, p, typed("x"), pop); cmd != nil {
		t.Error("a stray letter on an error screen should do nothing")
	}
	for _, k := range []tea.KeyMsg{enter, esc} {
		if !isPop(problemKey(sess, p, k, pop)) {
			t.Errorf("%q should go back", k.String())
		}
	}
}

func TestTrail(t *testing.T) {
	a := testApp()
	a = pidPrompt(t, a) // Inspect
	a, _ = sendApp(a, typed("w1"))
	a = navigate(t, a, enter) // PID w1, loading
	if got := a.trail(); !strings.Contains(got, "Inspect") || !strings.Contains(got, "PID w1") {
		t.Errorf("trail while loading = %q", got)
	}
	id := a.top().(objectScreen).id
	a, _ = sendApp(a, objectMsg{id: id, obj: atlas.Object{Type: atlas.TypeWork, NOID: "w1", Title: "Harbor"}})
	a = navigate(t, a, enter) // Record
	if got := stripANSI(a.trail()); got != "Inspect › Work w1 › Record" {
		t.Errorf("trail = %q", got)
	}
	if !strings.Contains(a.View(), "Inspect") {
		t.Errorf("header should carry the trail:\n%s", a.View())
	}

	a.sess.width = 22
	if got := stripANSI(a.trail()); got != "… › Record" && got != "… › Work w1 › Record" {
		t.Errorf("narrow trail = %q", got)
	}
	if w := lipgloss.Width(a.trail()); w > 21 {
		t.Errorf("narrow trail is %d wide, want ≤ 21", w)
	}
}

func TestViewKeepsClearOfLastColumn(t *testing.T) {
	a := testApp()
	a.sess.width = 30
	a, _ = sendApp(a, pushMsg{staticPager(a.sess, "MODS", "", textRender(strings.Repeat("x", 80)))})
	a, _ = sendApp(a, initResult[pagerMsg](t, a.top().Init()))
	for _, line := range strings.Split(a.View(), "\n") {
		if w := lipgloss.Width(line); w > 29 {
			t.Errorf("line is %d wide on a 30-column terminal: %q", w, line)
		}
	}
}

func TestExportDoneLeavesFarewell(t *testing.T) {
	s := newExportScreen(testSession())
	s.state, s.total, s.outDir = exportDownloading, 1, "./out"
	s.container.Title = "Maps"
	s, _ = sendExport(s, progressMsg{NOID: "a"})
	s, _ = sendExport(s, downloadDoneMsg{})
	if !strings.Contains(s.sess.farewell, `Saved 1 MODS file from "Maps" to ./out`) {
		t.Errorf("farewell = %q", s.sess.farewell)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
