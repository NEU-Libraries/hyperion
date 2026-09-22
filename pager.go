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
	"github.com/charmbracelet/x/ansi"
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

	wrapped bool  // whether the content was last laid out wrapped
	starts  []int // while wrapped: first view line of each document line
}

type pagerMsg struct {
	id     int64
	render renderFunc
	err    error
}

func newPagerScreen(sess *session, title, about string, load func(context.Context, *atlas.Client) (renderFunc, error)) pagerScreen {
	// The viewport pans ←/→ by its horizontal step, which starts at zero —
	// panning is off until it is set.
	vp := viewport.New(0, 0)
	vp.SetHorizontalStep(panStep)
	return pagerScreen{
		sess: sess, id: nextScreenID(), title: title, about: about, load: load,
		spinner: newSpinner(), vp: vp,
	}
}

// panStep is how many columns ←/→ moves a pager's view.
const panStep = 8

// staticPager is a pager whose content needs no request.
func staticPager(sess *session, title, about string, render renderFunc) pagerScreen {
	return newPagerScreen(sess, title, about, func(context.Context, *atlas.Client) (renderFunc, error) {
		return render, nil
	})
}

// textRender shows text as-is, trailing whitespace trimmed and tabs expanded.
// The viewport measures a tab as no width when it cuts a long line to fit, and
// lipgloss then draws it four wide — so a tabbed line would overflow and wrap.
func textRender(text string) renderFunc {
	text = strings.ReplaceAll(strings.TrimRight(text, " \n\t\r"), "\t", "    ")
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
		case "w":
			s.sess.wrap = !s.sess.wrap
			if s.state == loaded {
				s.layout()
			}
			return s, nil
		}
		var cmd tea.Cmd
		s.vp, cmd = s.vp.Update(msg)
		return s, cmd
	}
	return s, nil
}

// layout sizes the viewport to the terminal and re-renders into it.
//
// With wrap on, long lines fold under their own indent and there is nothing
// to pan; with it off they are cut at the edge and ←/→ pans. Either way the
// source line at the top of the view stays at the top.
func (s *pagerScreen) layout() {
	top := s.sourceLine(s.vp.YOffset)
	width := max(s.sess.width-1, 0) // the app keeps clear of the last column
	text := s.render(s.sess.width)

	s.vp.Width, s.vp.Height = width, bodyHeight(s.sess)
	s.wrapped = s.sess.wrap && width > 0
	if s.wrapped {
		text, s.starts = wrapLines(text, width)
		s.vp.SetXOffset(0)
		s.vp.SetHorizontalStep(0)
	} else {
		s.starts = nil
		s.vp.SetHorizontalStep(panStep)
	}
	s.vp.SetContent(text)
	s.vp.SetYOffset(s.displayLine(top))
}

// sourceLine maps a line of the view back to the line of the document it
// shows; displayLine maps the other way. They differ only while wrapping.
func (s pagerScreen) sourceLine(display int) int {
	if s.starts == nil {
		return display
	}
	src := 0
	for i, start := range s.starts {
		if start > display {
			break
		}
		src = i
	}
	return src
}

func (s pagerScreen) displayLine(src int) int {
	if s.starts == nil {
		return src
	}
	return s.starts[min(src, len(s.starts)-1)]
}

// wrapLines folds each line of text to width, continuing under the line's own
// indent (plus two) so nested XML still reads as nested. starts[i] is the
// index of the first wrapped line of source line i.
func wrapLines(text string, width int) (string, []int) {
	src := strings.Split(text, "\n")
	starts := make([]int, len(src))
	var out []string
	for i, line := range src {
		starts[i] = len(out)
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		plain := ansi.Strip(line)
		indent := len(plain) - len(strings.TrimLeft(plain, " "))
		hang := indent + 2
		if width-hang < 20 { // too deep to indent usefully
			indent, hang = 0, 0
		}
		first := ansi.Wrap(ansi.TruncateLeft(line, indent, ""), width-indent, "")
		for j, part := range strings.Split(first, "\n") {
			if j == 0 {
				out = append(out, strings.Repeat(" ", indent)+part)
				continue
			}
			// Continuation lines get the narrower hanging width.
			for _, p := range strings.Split(ansi.Wrap(part, width-hang, ""), "\n") {
				out = append(out, strings.Repeat(" ", hang)+p)
			}
		}
	}
	return strings.Join(out, "\n"), starts
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
	help := "↑/↓ scroll · ←/→ pan · w wrap · esc to go back"
	if s.wrapped {
		help = "↑/↓ scroll · w unwrap · esc to go back"
	}
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
