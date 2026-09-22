package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// screenSeq numbers screens so an async result is only applied by the screen
// that asked for it — a load that finishes after its screen was closed, or one
// meant for a sibling screen of the same type, is dropped.
var screenSeq atomic.Int64

func nextScreenID() int64 { return screenSeq.Add(1) }

type loadState int

const (
	loading loadState = iota
	loaded
	failed
)

// chromeLines is the vertical space around a section's body: the app header,
// the screen's title and context lines plus a gap (3), and the footer (3).
const chromeLines = headerLines + 6

// bodyHeight is how many lines a scrolling body may use. With no terminal size
// yet (tests, or before the first WindowSizeMsg) it is effectively unlimited.
func bodyHeight(sess *session) int {
	if sess.height <= 0 {
		return 1 << 20
	}
	return max(sess.height-chromeLines, 3)
}

// userErr is an error already worded by the caller for its context; explain
// shows it as-is.
type userErr string

func (e userErr) Error() string { return string(e) }

// renderFunc lays out loaded content for a given terminal width (0 = unknown),
// so a pager can re-flow it when the terminal is resized.
type renderFunc func(width int) string

// pagerScreen shows one loaded document — MODS XML, METS, a record's fields —
// in a scrolling viewport. Long lines are cut at the edge and pan with ←/→.
type pagerScreen struct {
	sess  *session
	id    int64
	title string
	about string
	load  func(context.Context, *atlas.Client) (renderFunc, error)

	state   loadState
	render  renderFunc
	prob    problem
	spinner spinner.Model
	vp      viewport.Model
}

type pagerMsg struct {
	id     int64
	render renderFunc
	err    error
}

func newPagerScreen(sess *session, title, about string, load func(context.Context, *atlas.Client) (renderFunc, error)) pagerScreen {
	return pagerScreen{
		sess: sess, id: nextScreenID(), title: title, about: about, load: load,
		spinner: newSpinner(), vp: viewport.New(0, 0),
	}
}

// staticPager is a pager whose content needs no request.
func staticPager(sess *session, title, about string, render renderFunc) pagerScreen {
	return newPagerScreen(sess, title, about, func(context.Context, *atlas.Client) (renderFunc, error) {
		return render, nil
	})
}

// textRender shows text as-is, trailing whitespace trimmed.
func textRender(text string) renderFunc {
	text = strings.TrimRight(text, " \n\t\r")
	return func(int) string { return text }
}

func (s pagerScreen) Init() tea.Cmd {
	id, client, load := s.id, s.sess.client, s.load
	return tea.Batch(s.spinner.Tick, func() tea.Msg {
		r, err := load(context.Background(), client)
		return pagerMsg{id: id, render: r, err: err}
	})
}

func (s pagerScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case pagerMsg:
		if msg.id != s.id {
			return s, nil
		}
		if msg.err != nil {
			s.prob = explain("Couldn't load "+s.title+".", msg.err)
			s.state = failed
			return s, nil
		}
		s.render = msg.render
		s.state = loaded
		s.layout()
		return s, nil
	case tea.WindowSizeMsg:
		if s.state == loaded {
			s.layout()
		}
		return s, nil
	case spinner.TickMsg:
		if s.state != loading {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyMsg:
		if s.state == failed {
			return s, problemKey(s.sess, s.prob, msg, pop)
		}
		switch msg.String() {
		case "esc", "q":
			return s, pop
		case "home", "g":
			s.vp.GotoTop()
			return s, nil
		case "end", "G":
			s.vp.GotoBottom()
			return s, nil
		}
		var cmd tea.Cmd
		s.vp, cmd = s.vp.Update(msg)
		return s, cmd
	}
	return s, nil
}

// layout sizes the viewport to the terminal and re-renders into it.
func (s *pagerScreen) layout() {
	s.vp.Width = s.sess.width
	s.vp.Height = bodyHeight(s.sess)
	s.vp.SetContent(s.render(s.sess.width))
}

func (s pagerScreen) crumb() string { return s.title }

func (s pagerScreen) View() string {
	head := titleStyle.Render(s.title) + "\n" + dimStyle.Render(truncate(s.about, s.sess.width)) + "\n\n"
	switch s.state {
	case loading:
		return head + s.spinner.View() + " Loading…"
	case failed:
		return head + s.prob.view(s.sess.width, "go back")
	}
	help := "↑/↓ scroll · ←/→ pan · esc to go back"
	if !(s.vp.AtTop() && s.vp.AtBottom()) {
		help = fmt.Sprintf("%3.0f%% · ", s.vp.ScrollPercent()*100) + help
	}
	return head + s.vp.View() + "\n\n" + dimStyle.Render(help)
}

// fieldsRender lays out record fields as an aligned label/value table, wrapping
// long values under their own column rather than cutting them.
func fieldsRender(fields []atlas.Field) renderFunc {
	return func(width int) string {
		labelWidth := 0
		for _, f := range fields {
			labelWidth = max(labelWidth, lipgloss.Width(fieldLabel(f.Key)))
		}
		valueWidth := 0
		if width > 0 {
			valueWidth = max(width-labelWidth-2, 10)
		}
		indent := strings.Repeat(" ", labelWidth+2)

		var b strings.Builder
		for _, f := range fields {
			value := fieldValue(f.Key, f.Value, 0)
			if valueWidth > 0 {
				value = lipgloss.NewStyle().Width(valueWidth).Render(value)
			}
			lines := strings.Split(value, "\n")
			b.WriteString(dimStyle.Render(fmt.Sprintf("%-*s", labelWidth, fieldLabel(f.Key))) + "  " + lines[0] + "\n")
			for _, l := range lines[1:] {
				b.WriteString(indent + l + "\n")
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}
}
