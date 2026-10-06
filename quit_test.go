package main

import (
	"context"
	"strings"
	"testing"

	"github.com/NEU-Libraries/hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	a = pidPrompt(t, a) // the PID prompt
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

// keyRow is the last non-blank line of the app's view: the key help.
func keyRow(a app) string {
	lines := strings.Split(strings.TrimRight(ansi.Strip(a.View()), "\n "), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func TestTheKeyRowEndsWithHowToQuit(t *testing.T) {
	menu := testApp()
	if row := keyRow(menu); row != "↑/↓ to move · enter to select · q to quit" {
		t.Errorf("menu key row = %q", row)
	}
	if strings.Contains(strings.Split(ansi.Strip(menu.View()), "\n")[0], "quit") {
		t.Error("the header should no longer carry the quit hint")
	}
	if row := keyRow(deepApp(t)); !strings.HasSuffix(row, "esc to go back · h to go home · q to quit") {
		t.Errorf("MODS key row = %q", row)
	}
	prompt := pidPrompt(t, testApp())
	if row := keyRow(prompt); row != "enter to inspect · esc to go back · ctrl+c to quit" {
		t.Errorf("while typing, the key row = %q", row)
	}
	asked, _ := sendApp(deepApp(t), typed("q"))
	if row := keyRow(asked); row != "y to quit · n or esc to stay" {
		t.Errorf("the quit question's key row = %q", row)
	}

	// A screen still loading has no key row of its own; the hint stands alone.
	loading := testApp()
	loading = pidPrompt(t, loading)
	loading, _ = sendApp(loading, typed("w1"))
	loading = navigate(t, loading, enter)
	if row := keyRow(loading); row != "h to go home · q to quit" {
		t.Errorf("a loading screen's key row = %q", row)
	}
}

func TestALongKeyRowCompactsRatherThanLoseTheQuitHint(t *testing.T) {
	a := testApp()
	a.sess.width, a.sess.height = 80, 30
	a, _ = sendApp(a, pushMsg{newPagerScreen(a.sess, "MODS", "", func(context.Context, *atlas.Client) (doc, error) {
		long := strings.Repeat("<mods:note>a long line of MODS</mods:note>", 3)
		return doc{render: textRender(strings.Repeat(long+"\n", 40)), raw: []byte(long)}, nil
	}).withRaw("/resources/w1/mods.xml", "w1-mods")})
	a, _ = sendApp(a, initResult[pagerMsg](t, a.top().Init()))
	row := keyRow(a)
	if !strings.HasSuffix(row, "esc back · q quit") || lipgloss.Width(row) > 79 {
		t.Errorf("at 80 columns the full pager key row should compact and keep the hint: %q (%d wide)", row, lipgloss.Width(row))
	}
	a.sess.width = 120
	a, _ = sendApp(a, tea.WindowSizeMsg{Width: 120, Height: 30})
	if row := keyRow(a); !strings.HasSuffix(row, "esc to go back · h to go home · q to quit") {
		t.Errorf("with room, the full wording should stay: %q", row)
	}
}

func TestHGoesHomeFromAnyScreen(t *testing.T) {
	a := deepApp(t)
	home, _ := sendApp(a, typed("h"))
	if len(home.stack) != 1 {
		t.Fatalf("h should leave only the menu, not %d screens", len(home.stack))
	}
	if _, ok := home.top().(menuScreen); !ok {
		t.Fatalf("h should land on the menu, not %T", home.top())
	}
	if row := keyRow(home); row != "↑/↓ to move · enter to select · q to quit" {
		t.Errorf("the menu shouldn't offer h, being home already: %q", row)
	}
	if again, _ := sendApp(home, typed("h")); len(again.stack) != 1 {
		t.Error("h on the menu should do nothing")
	}
}

func TestHIsALetterWhileTyping(t *testing.T) {
	a := pidPrompt(t, testApp()) // the PID prompt
	a, _ = sendApp(a, typed("h"))
	if len(a.stack) != 2 || a.top().(pidPromptScreen).input.Value() != "h" {
		t.Error("h in the PID prompt should type an h")
	}
}

func TestHWaitsForARunningExport(t *testing.T) {
	a := testApp()
	s := newExportScreen(a.sess)
	s.state, s.total, s.completed, s.outDir = exportDownloading, 10, 3, "./z612nb3-mods"
	a.stack = append(a.stack, s)
	if a, _ = sendApp(a, typed("h")); len(a.stack) != 2 {
		t.Error("h shouldn't leave an export that is still running")
	}
	if strings.Contains(keyRow(a), "home") {
		t.Errorf("a running export shouldn't offer h: %q", keyRow(a))
	}
	s.state = exportDone
	a.stack[1] = s
	if a, _ = sendApp(a, typed("h")); len(a.stack) != 1 {
		t.Error("h should go home once the export is done")
	}
}
