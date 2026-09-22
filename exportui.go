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
	pid.Prompt = "PID: "
	pid.Placeholder = "e.g. gqnkbq9"
	pid.CharLimit = 64

	out := textinput.New()
	out.Prompt = "Save to: "

	prog := progress.New(progress.WithDefaultGradient())
	if sess.width > 0 {
		prog.Width = min(sess.width-4, 60)
	}
	return exportScreen{sess: sess, pidInput: pid, outInput: out, spinner: newSpinner(), prog: prog}
}

func (s exportScreen) Init() tea.Cmd { return nil }

func (s exportScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.prog.Width = min(msg.Width-4, 60)
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
			s.pidInput.Focus()
			s.state = exportPID
			return s, textinput.Blink
		case "q", "esc":
			return s, pop
		}
		return s, nil

	case exportPID:
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

	case exportOut:
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

	case exportConfirm:
		switch msg.String() {
		case "y", "Y", "enter":
			return s.startDownload()
		default:
			return s, pop
		}

	case exportDone:
		if isBackKey(msg) {
			return s, pop
		}
		return s, nil
	case exportError:
		return s, problemKey(s.sess, s.prob, msg, pop)
	}
	return s, nil
}

func (s exportScreen) resolveCmd() tea.Cmd {
	kind := atlas.Collection
	if s.kindIdx == 1 {
		kind = atlas.Set
	}
	client, pid := s.sess.client, s.pidInput.Value()
	return func() tea.Msg {
		c, err := resolve(context.Background(), client, kind, pid)
		return resolvedMsg{container: c, err: err, kind: kind, pid: pid}
	}
}

func (s exportScreen) handleResolved(msg resolvedMsg) (screen, tea.Cmd) {
	switch {
	case msg.err != nil:
		s.prob = resolveProblem(msg.err, msg.kind, msg.pid)
		s.state = exportError
	case len(msg.container.Members) == 0:
		s.prob = problem{headline: "Nothing to export.", reason: fmt.Sprintf("That %s has no members this token can read.", msg.kind)}
		s.state = exportError
	default:
		s.container = msg.container
		s.total = len(msg.container.Members)
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
	switch s.state {
	case exportType:
		return "Export the MODS of every member of a…\n\n" + menuView([]string{"Collection", "Set"}, s.kindIdx) +
			"\n" + dimStyle.Render("↑/↓ to move · enter to select · esc to go back")
	case exportPID:
		return s.pidInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to go back")
	case exportOut:
		return s.outInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to go back")
	case exportResolving:
		return fmt.Sprintf("%s Resolving %s %s…", s.spinner.View(), s.kindLabel(), s.pidInput.Value())
	case exportConfirm:
		return s.viewConfirm()
	case exportDownloading:
		return s.viewDownloading()
	case exportDone:
		return s.viewDone()
	default:
		return s.prob.view(s.sess.cols(), backKeys)
	}
}

func (s exportScreen) viewConfirm() string {
	return fmt.Sprintf("%s %s\n%s\n\nDownload? %s",
		okStyle.Render("✓"), s.container.Title,
		dimStyle.Render(fmt.Sprintf("%d %s → %s", s.total, records(s.total), s.outDir)),
		dimStyle.Render("(y/n)"))
}

func (s exportScreen) viewDownloading() string {
	ratio := 0.0
	if s.total > 0 {
		ratio = float64(s.completed) / float64(s.total)
	}
	return fmt.Sprintf("Downloading MODS…\n\n%s\n%s",
		s.prog.ViewAs(ratio),
		dimStyle.Render(fmt.Sprintf("%d/%d", s.completed, s.total)))
}

func (s exportScreen) viewDone() string {
	return s.summary() + "\n\n" + dimStyle.Render(backKeys)
}

// summary tallies the finished export. It is also left on the terminal when
// hyperion exits, since the alternate screen takes the TUI's copy with it.
func (s exportScreen) summary() string {
	saved := s.total - len(s.failures)
	var b strings.Builder
	b.WriteString(okStyle.Render(fmt.Sprintf("✓ Saved %d %s from %q to %s", saved, records(saved), s.container.Title, s.outDir)))
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

func (s exportScreen) crumb() string { return "Export MODS" }

func (s exportScreen) kindLabel() string {
	if s.kindIdx == 1 {
		return "set"
	}
	return "collection"
}
