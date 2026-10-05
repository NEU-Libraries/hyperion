package main

import (
	"context"
	"fmt"
	"strings"

	"hyperion/internal/atlas"
	"hyperion/internal/export"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type exportState int

const (
	exportType exportState = iota
	exportPID
	exportOut
	exportResolving
	exportConfirm
	exportDownloading
	exportDone
	exportError
)

type failure struct {
	noid string
	err  error
}

// exportScreen is the bulk MODS export flow: pick Collection or Set, take the
// PID and output directory, confirm, then download with a progress bar. Esc
// steps back through the prompts and finally returns to the menu.
type exportScreen struct {
	sess  *session
	state exportState
	prob  problem

	pidInput textinput.Model
	outInput textinput.Model
	spinner  spinner.Model
	prog     progress.Model

	kindIdx   int // 0 = Collection, 1 = Set
	container atlas.Container
	outDir    string
	existing  int // members whose file is already in outDir, to be replaced

	sub       chan export.Progress
	total     int
	completed int
	failures  []failure
}

type (
	resolvedMsg struct {
		container atlas.Container
		err       error
		kind      atlas.Kind
		pid       string
	}
	progressMsg     export.Progress
	downloadDoneMsg struct{}
)

func newExportScreen(sess *session) exportScreen {
	pid := textinput.New()
	pid.Prompt = "Collection PID: "
	pid.Placeholder = "e.g. gqnkbq9"
	pid.CharLimit = 64

	out := textinput.New()
	out.Prompt = "Folder: "

	prog := progress.New(progress.WithDefaultGradient())
	prog.Width = min(frameWidth(sess)-frameCols, 60) // inside the frame
	return exportScreen{sess: sess, pidInput: pid, outInput: out, spinner: newSpinner(), prog: prog}
}

func (s exportScreen) Init() tea.Cmd { return nil }

func (s exportScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.prog.Width = min(frameWidth(s.sess)-frameCols, 60)
		return s, nil
	case tea.KeyMsg:
		return s.handleKey(msg)
	case spinner.TickMsg:
		if s.state != exportResolving {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case resolvedMsg:
		return s.handleResolved(msg)
	case progressMsg:
		s.completed++
		if msg.Err != nil {
			s.failures = append(s.failures, failure{noid: msg.NOID, err: msg.Err})
		}
		return s, waitForProgress(s.sub)
	case downloadDoneMsg:
		s.state = exportDone
		s.sess.farewell = s.summary()
		return s, nil
	}

	var cmd tea.Cmd
	switch s.state {
	case exportPID:
		s.pidInput, cmd = s.pidInput.Update(msg)
	case exportOut:
		s.outInput, cmd = s.outInput.Update(msg)
	}
	return s, cmd
}

func (s exportScreen) handleKey(msg tea.KeyMsg) (screen, tea.Cmd) {
	switch s.state {
	case exportType:
		switch msg.String() {
		case "up", "k":
			s.kindIdx = max(s.kindIdx-1, 0)
		case "down", "j":
			s.kindIdx = min(s.kindIdx+1, 1)
		case "enter":
			return s.toPID()
		case "esc":
			return s, pop
		}
		return s, nil

	case exportPID:
		return s.pidKey(msg)

	case exportOut:
		return s.outKey(msg)

	case exportConfirm:
		switch msg.String() {
		case "y", "Y", "enter":
			return s.startDownload()
		case "n", "N", "esc":
			// Back to the folder, everything kept, to change it or go back further.
			s.outInput.Focus()
			s.state = exportOut
			return s, textinput.Blink
		}
		return s, nil

	case exportDone:
		if isBackKey(msg) {
			return s, pop
		}
		return s, nil
	case exportError:
		// Back to the PID, kept: the likely fix is a typo or the other type.
		if isBackKey(msg) {
			return s.toPID()
		}
		return s, problemKey(s.sess, s.prob, msg, nil)
	}
	return s, nil
}

// pidKey handles the PID step: enter moves on to the folder, which defaults
// to ./<pid>-mods; esc goes back to the export type.
func (s exportScreen) pidKey(msg tea.KeyMsg) (screen, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		v := strings.TrimSpace(s.pidInput.Value())
		if v == "" {
			return s, nil
		}
		s.pidInput.SetValue(v)
		s.pidInput.Blur()
		s.outInput.SetValue("./" + v + "-mods")
		s.outInput.Focus()
		s.outInput.CursorEnd()
		s.state = exportOut
		return s, textinput.Blink
	case tea.KeyEsc:
		s.pidInput.Blur()
		s.state = exportType
		return s, nil
	}
	var cmd tea.Cmd
	s.pidInput, cmd = s.pidInput.Update(msg)
	return s, cmd

}

// outKey handles the folder step: enter resolves the export; esc goes back
// to the PID.
func (s exportScreen) outKey(msg tea.KeyMsg) (screen, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		s.outDir = strings.TrimSpace(s.outInput.Value())
		if s.outDir == "" {
			return s, nil
		}
		s.outInput.Blur()
		s.state = exportResolving
		return s, tea.Batch(s.spinner.Tick, s.resolveCmd())
	case tea.KeyEsc:
		s.outInput.Blur()
		s.pidInput.Focus()
		s.state = exportPID
		return s, textinput.Blink
	}
	var cmd tea.Cmd
	s.outInput, cmd = s.outInput.Update(msg)
	return s, cmd

}

// toPID opens the PID prompt, labelled for the chosen type.
func (s exportScreen) toPID() (screen, tea.Cmd) {
	s.pidInput.Prompt = s.kindName() + " PID: "
	s.pidInput.CursorEnd()
	s.pidInput.Focus()
	s.state = exportPID
	return s, textinput.Blink
}

func (s exportScreen) resolveCmd() tea.Cmd {
	kind := atlas.Collection
	if s.kindIdx == 1 {
		kind = atlas.Set
	}
	client, pid := s.sess.client, s.pidInput.Value()
	return func() tea.Msg {
		c, err := resolveForExport(context.Background(), client, kind, pid)
		return resolvedMsg{container: c, err: err, kind: kind, pid: pid}
	}
}

func (s exportScreen) handleResolved(msg resolvedMsg) (screen, tea.Cmd) {
	switch {
	case msg.err != nil:
		s.prob = resolveProblem(msg.err, msg.kind, msg.pid)
		s.state = exportError
	case len(msg.container.Members) == 0:
		s.prob = problem{headline: "Nothing to export.",
			reason: fmt.Sprintf("This %s has no members your account can view.", msg.kind)}
		s.state = exportError
	default:
		s.container = msg.container
		s.total = len(msg.container.Members)
		s.existing = export.Existing(s.outDir, msg.container.Members)
		s.state = exportConfirm
	}
	return s, nil
}

func (s exportScreen) startDownload() (screen, tea.Cmd) {
	s.sub = make(chan export.Progress)
	client, members, dir, conc, sub := s.sess.client, s.container.Members, s.outDir, s.sess.concurrency, s.sub
	go func() {
		_, _ = export.Download(context.Background(), client, members, dir, conc, sub)
		close(sub)
	}()
	s.state = exportDownloading
	return s, waitForProgress(sub)
}

// waitForProgress blocks on the next Progress and turns it into a msg; a closed
// channel (download finished) becomes downloadDoneMsg. Re-issued each Update to
// pull the following event — the canonical Bubble Tea real-time-activity pattern.
func waitForProgress(sub chan export.Progress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-sub
		if !ok {
			return downloadDoneMsg{}
		}
		return progressMsg(p)
	}
}

func (s exportScreen) View() string {
	title := titleStyle.Render(s.crumb())
	switch s.state {
	case exportType:
		return panel(s.sess, title, "Export the MODS of every member of a…\n\n"+
			strings.TrimRight(menuView([]string{"Collection", "Set"}, s.kindIdx), "\n"),
			"↑/↓ to move · enter to select · esc to go back")
	case exportPID:
		return panel(s.sess, title, fmt.Sprintf("Which %s? Enter its PID.\n\n", strings.ToLower(s.kindName()))+s.pidInput.View(),
			"enter to continue · esc to go back")
	case exportOut:
		return panel(s.sess, title, "Save the MODS files in this folder:\n\n"+s.outInput.View(),
			"enter to continue · esc to go back")
	case exportResolving:
		return panel(s.sess, title, fmt.Sprintf("%s Looking up %s %s…", s.spinner.View(), s.kindName(), s.pidInput.Value()), "")
	case exportConfirm:
		return panel(s.sess, title, s.viewConfirm(), "y to export · n to go back")
	case exportDownloading:
		return panel(s.sess, title, s.viewDownloading(), "ctrl+c stops the export and quits Hyperion")
	case exportDone:
		return panel(s.sess, title, wrapEach(s.summary(), frameWidth(s.sess)-frameCols), backKeys)
	default:
		return s.prob.panel(s.sess, title, backKeys)
	}
}

// viewConfirm asks before exporting. Its sentences carry a folder path of
// any length, so they wrap inside the frame rather than being cut.
func (s exportScreen) viewConfirm() string {
	width := frameWidth(s.sess) - frameCols
	out := wrapped(lipgloss.NewStyle(),
		fmt.Sprintf("Export %s from %q to %s?", plural(s.total, "MODS file"), s.container.Title, s.outDir), width)
	if s.existing > 0 {
		out += "\n\n" + wrapped(warnStyle,
			fmt.Sprintf("⚠ %s already has %d of these files; they will be replaced.", s.outDir, s.existing), width)
	}
	return out
}

func (s exportScreen) viewDownloading() string {
	ratio := 0.0
	if s.total > 0 {
		ratio = float64(s.completed) / float64(s.total)
	}
	return fmt.Sprintf("Exporting MODS…\n\n%s\n%s",
		s.prog.ViewAs(ratio),
		dimStyle.Render(fmt.Sprintf("%d of %d", s.completed, s.total)))
}

// summary tallies the finished export. It is also left on the terminal when
// hyperion exits, since the alternate screen takes the TUI's copy with it.
func (s exportScreen) summary() string {
	saved := s.total - len(s.failures)
	line := fmt.Sprintf("Saved %s from %q to %s", plural(saved, "MODS file"), s.container.Title, s.outDir)
	var b strings.Builder
	switch {
	case len(s.failures) == 0:
		b.WriteString(okStyle.Render("✓ " + line))
	case saved == 0:
		b.WriteString(errStyle.Render("✗ " + line))
	default:
		b.WriteString(warnStyle.Render(fmt.Sprintf("⚠ Saved %d of %d MODS files from %q to %s", saved, s.total, s.container.Title, s.outDir)))
	}
	if len(s.failures) > 0 {
		b.WriteString("\n" + warnStyle.Render(fmt.Sprintf("⚠ %d could not be exported:", len(s.failures))))
		for i, f := range s.failures {
			if i >= 10 {
				b.WriteString("\n" + warnStyle.Render(fmt.Sprintf("  …and %d more", len(s.failures)-10)))
				break
			}
			b.WriteString("\n" + warnStyle.Render(fmt.Sprintf("  %s: %s", f.noid, briefError(f.err))))
		}
	}
	return b.String()
}

// crumb names the export once it has a target: "Export MODS: Set sn02xjb".
func (s exportScreen) crumb() string {
	if s.state > exportPID {
		return "Export MODS: " + s.kindName() + " " + s.pidInput.Value()
	}
	return "Export MODS"
}

func (s exportScreen) typing() bool { return s.state == exportPID || s.state == exportOut }

// quitWarning says what quitting mid-export would leave behind.
func (s exportScreen) quitWarning() string {
	if s.state != exportDownloading {
		return ""
	}
	return fmt.Sprintf("An export is still running. Quitting stops it: %d of %d MODS files are saved in %s so far.",
		s.completed, s.total, s.outDir)
}

func (s exportScreen) kindName() string {
	if s.kindIdx == 1 {
		return "Set"
	}
	return "Collection"
}
