package main

import (
	"errors"
	"strings"
	"testing"

	"hyperion/internal/atlas"
	"hyperion/internal/export"

	tea "github.com/charmbracelet/bubbletea"
)

// These drive the Bubble Tea model's Update directly with the messages its
// commands would produce (KeyMsg, resolvedMsg, progressMsg, downloadDoneMsg),
// so the state machine and views are exercised without a terminal or network.

// testModel starts already authenticated (at the type picker), so the export
// flow tests don't need to run the network-backed auth command.
func testModel() model {
	m := newModel("http://example", "tok", 4)
	m.client = atlas.New("http://example", "tok")
	m.user = atlas.User{Name: "Doe, Jane", NUID: "000000002"}
	m.state = stateType
	return m
}

func send(m model, msg tea.Msg) model {
	next, _ := m.Update(msg)
	return next.(model)
}

func TestCredentialPromptFlow(t *testing.T) {
	// No creds → prompt for URL (prefilled default), then token, then auth.
	m := newModel("", "", 4)
	if m.state != stateURL {
		t.Fatalf("no creds: state = %v, want stateURL", m.state)
	}
	if m.urlInput.Value() != defaultURL {
		t.Errorf("URL not prefilled: %q", m.urlInput.Value())
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter}) // accept default URL
	if m.state != stateToken {
		t.Fatalf("after URL: state = %v, want stateToken", m.state)
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jwt.abc.123")})
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateAuth {
		t.Fatalf("after token: state = %v, want stateAuth", m.state)
	}

	// Stand in for the auth command's result.
	ok := send(m, authMsg{user: atlas.User{Name: "Doe, Jane", NUID: "000000002"}, client: atlas.New("u", "t")})
	if ok.state != stateType {
		t.Fatalf("auth ok: state = %v, want stateType", ok.state)
	}

	// With both creds present, the prompts are skipped entirely.
	if s := newModel("u", "t", 4).state; s != stateAuth {
		t.Errorf("creds present: initial state = %v, want stateAuth", s)
	}
	// A rejected token surfaces as an error, not a silent pass.
	bad := send(newModel("u", "t", 4), authMsg{err: atlas.ErrUnauthorized})
	if bad.state != stateError {
		t.Errorf("auth fail: state = %v, want stateError", bad.state)
	}
}

func TestFlowToConfirm(t *testing.T) {
	m := testModel()
	if m.state != stateType {
		t.Fatalf("initial state = %v, want stateType", m.state)
	}
	if v := m.View(); !strings.Contains(v, "HYPERION") || !strings.Contains(v, "Doe, Jane") {
		t.Errorf("header missing name/title:\n%s", v)
	}

	m = send(m, tea.KeyMsg{Type: tea.KeyEnter}) // choose Collection
	if m.state != statePID {
		t.Fatalf("after enter, state = %v, want statePID", m.state)
	}
	if !strings.Contains(m.View(), "PID:") {
		t.Errorf("PID prompt missing:\n%s", m.View())
	}

	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("gqnkbq9")})
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateOut {
		t.Fatalf("after PID, state = %v, want stateOut", m.state)
	}
	if got := m.outInput.Value(); got != "./gqnkbq9-mods" {
		t.Errorf("default out dir = %q, want ./gqnkbq9-mods", got)
	}

	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateResolving {
		t.Fatalf("after out, state = %v, want stateResolving", m.state)
	}

	// Stand in for the resolve command's result.
	m = send(m, resolvedMsg{
		container: atlas.Container{Kind: atlas.Collection, Title: "Test Collection",
			Members: []atlas.Member{{NOID: "a"}, {NOID: "b"}, {NOID: "c"}}},
		kind: atlas.Collection, pid: "gqnkbq9",
	})
	if m.state != stateConfirm || m.total != 3 {
		t.Fatalf("after resolved: state=%v total=%d", m.state, m.total)
	}
	if v := m.View(); !strings.Contains(v, "Test Collection") || !strings.Contains(v, "3 records") {
		t.Errorf("confirm view:\n%s", v)
	}
}

func TestResolveErrors(t *testing.T) {
	base := testModel()
	base.state = stateResolving

	notFound := send(base, resolvedMsg{err: atlas.ErrNotFound, kind: atlas.Collection, pid: "zzz"})
	if notFound.state != stateError || !strings.Contains(notFound.View(), "PID zzz") {
		t.Errorf("not-found: state=%v view=%q", notFound.state, notFound.View())
	}

	empty := send(base, resolvedMsg{container: atlas.Container{Kind: atlas.Set}, kind: atlas.Set, pid: "x"})
	if empty.state != stateError || !strings.Contains(empty.View(), "no readable members") {
		t.Errorf("empty: state=%v view=%q", empty.state, empty.View())
	}
}

func TestProgressTally(t *testing.T) {
	m := testModel()
	m.state = stateDownloading
	m.total = 3
	m.outDir = "./out"

	m = send(m, progressMsg(export.Progress{NOID: "a"}))
	m = send(m, progressMsg(export.Progress{NOID: "b", Err: errors.New("boom")}))
	if m.completed != 2 || len(m.failures) != 1 {
		t.Fatalf("completed=%d failures=%d, want 2/1", m.completed, len(m.failures))
	}

	m = send(m, downloadDoneMsg{})
	if m.state != stateDone {
		t.Fatalf("after done, state=%v want stateDone", m.state)
	}
	if v := m.View(); !strings.Contains(v, "Saved 2") || !strings.Contains(v, "1 could not") {
		t.Errorf("done view:\n%s", v)
	}
}
