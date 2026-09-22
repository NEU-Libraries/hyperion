package main

import (
	"errors"
	"strings"
	"testing"

	"hyperion/internal/atlas"

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
