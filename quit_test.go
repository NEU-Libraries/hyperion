package main

import (
	"context"
	"strings"
	"testing"

	"hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// deepApp is signed in and three screens deep: menu › Inspect › Work › MODS.
func deepApp(t *testing.T) app {
	t.Helper()
	a := workOverview(t, 100, 40, "admin")
	a, _ = sendApp(a, pushMsg{newPagerScreen(a.sess, "MODS", "Work w1 · Harbor", func(context.Context, *atlas.Client) (doc, error) {
		return doc{render: textRender("<mods/>"), raw: []byte("<mods/>")}, nil
	})})
	a, _ = sendApp(a, initResult[pagerMsg](t, a.top().Init()))
	return a
}

func TestQuitFromAnyScreenAsksFirst(t *testing.T) {
	a := deepApp(t)
	depth := len(a.stack)

	asked, cmd := sendApp(a, typed("q"))
	if cmd != nil || !asked.quitting {
		t.Fatal("q should ask, not quit")
	}
	v := ansi.Strip(asked.View())
	for _, want := range []string{"Quit Hyperion?", "loses nothing but your place", "y to quit · n or esc to stay",
		"Inspect › Work w1 › MODS"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirmation missing %q:\n%s", want, v)
		}
	}
	if _, cmd := sendApp(asked, enter); cmd != nil {
		t.Error("keys other than y, n, esc, and q should do nothing while asking")
	}

	for _, stay := range []tea.KeyMsg{typed("n"), esc, typed("q")} {
		back, cmd := sendApp(asked, stay)
		if cmd != nil || back.quitting || len(back.stack) != depth {
			t.Errorf("%q should stay where you were: quitting=%v depth=%d", stay.String(), back.quitting, len(back.stack))
		}
		if !strings.Contains(ansi.Strip(back.View()), "<mods/>") {
			t.Errorf("%q should return to the MODS screen:\n%s", stay.String(), ansi.Strip(back.View()))
		}
	}
	if _, cmd := sendApp(asked, typed("y")); !isQuit(cmd) {
		t.Error("y should quit")
	}
	if _, cmd := sendApp(a, tea.KeyMsg{Type: tea.KeyCtrlC}); !isQuit(cmd) {
		t.Error("ctrl+c should still quit at once")
	}
}

func TestQIsALetterWhileTyping(t *testing.T) {
	a := testApp()
	a = navigate(t, a, enter) // the PID prompt
	if !strings.Contains(ansi.Strip(a.View()), "ctrl+c to quit") {
		t.Errorf("while typing, the header should point at ctrl+c:\n%s", ansi.Strip(a.View()))
	}
	a, _ = sendApp(a, typed("q"))
	if a.quitting || a.top().(pidPromptScreen).input.Value() != "q" {
		t.Error("q in the PID prompt should type a q")
	}

	d := deepApp(t)
	if !strings.Contains(ansi.Strip(d.View()), "q to quit") {
		t.Error("off a text field, the header should say q quits")
	}
	d, _ = sendApp(d, typed("s")) // open the save prompt on MODS
	d, _ = sendApp(d, typed("q"))
	if d.quitting || !strings.HasSuffix(d.top().(pagerScreen).save.input.Value(), "q") {
		t.Error("q in the save prompt should type a q")
	}
}

func TestQuittingMidExportWarnsAndTheExportCarriesOn(t *testing.T) {
	a := testApp()
	s := newExportScreen(a.sess)
	s.state, s.total, s.completed, s.outDir = exportDownloading, 10, 3, "./z612nb3-mods"
	a.stack = append(a.stack, s)

	a, _ = sendApp(a, typed("q"))
	v := strings.Join(strings.Fields(unframe(ansi.Strip(a.View()))), " ")
	if !strings.Contains(v, "An export is still running. Quitting stops it: 3 of 10 MODS files are saved in ./z612nb3-mods so far.") {
		t.Errorf("mid-export confirmation:\n%s", ansi.Strip(a.View()))
	}
	a, _ = sendApp(a, progressMsg{NOID: "d"})
	if got := a.top().(exportScreen).completed; got != 4 || !a.quitting {
		t.Errorf("the export should carry on while asking: completed=%d quitting=%v", got, a.quitting)
	}
}
