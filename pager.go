package main

import (
	"context"
	"errors"
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

// notice is a load that found nothing to show, for an ordinary reason — an
// object with no METS, a section the account can't open — rather than a
// failure. Pagers and lists show it calmly, like an empty list, not as ✗.
type notice string

func (n notice) Error() string { return string(n) }

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
	load  func(context.Context, *atlas.Client) (doc, error)

	// rawPath is the Atlas route r shows raw ("" = no raw view); rawName is
	// the stem a save of it is offered under. isRaw marks the raw view itself,
	// where r goes back.
	rawPath, rawName string
	rawTitle         string // what the raw response is ("" = the pager's title)
	rawBody          []byte // the raw response, when already in hand
	isRaw            bool

	state   loadState
	render  renderFunc
	raw     []byte // the bytes s saves; nil when the pager shows no document
	prob    problem
	save    saveState
	spinner spinner.Model
	vp      viewport.Model

	wrapped bool  // whether the content was last laid out wrapped
	cut     bool  // whether, unwrapped, some line is wider than the view
	starts  []int // while wrapped: first view line of each document line
}

type pagerMsg struct {
	id  int64
	doc doc
	err error
}

// doc is what a pager's load produces: how to lay it out, and — when it is a
// document Atlas sent — the bytes exactly as sent, for saving.
type doc struct {
	render renderFunc
	raw    []byte
}

func newPagerScreen(sess *session, title, about string, load func(context.Context, *atlas.Client) (doc, error)) pagerScreen {
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
	return newPagerScreen(sess, title, about, func(context.Context, *atlas.Client) (doc, error) {
		return doc{render: render}, nil
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
		d, err := load(context.Background(), client)
		return pagerMsg{id: id, doc: d, err: err}
	})
}

func (s pagerScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case pagerMsg:
		if msg.id != s.id {
			return s, nil
		}
		var n notice
		if errors.As(msg.err, &n) {
			msg.doc, msg.err = doc{render: textRender(dimStyle.Render(string(n)))}, nil
		}
		if msg.err != nil {
			s.prob = explain("Couldn't load "+s.title+".", msg.err)
			s.state = failed
			return s, nil
		}
		s.render, s.raw = msg.doc.render, msg.doc.raw
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
		if s.save.active {
			var cmd tea.Cmd
			s.save, cmd = s.save.update(msg, s.raw)
			return s, cmd
		}
		s.save.notice = ""
		switch msg.String() {
		case "esc", "q":
			return s, pop
		case "r":
			switch {
			case s.isRaw:
				return s, pop
			case s.rawPath != "":
				return s, push(newRawPager(s.sess, firstNonEmpty(s.rawTitle, s.title), s.rawPath, s.rawName, s.rawBody))
			}
			return s, nil
		case "s":
			if s.state == loaded && s.raw != nil {
				return s, s.save.start(s.rawName, s.raw)
			}
			return s, nil
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
	width := s.sess.cols()
	text := s.render(width)

	s.cut = false
	for _, l := range strings.Split(text, "\n") {
		if width > 0 && ansi.StringWidth(l) > width {
			s.cut = true
			break
		}
	}
	s.vp.Width = width
	s.wrapped = s.sess.wrap && s.cut
	if s.wrapped {
		text, s.starts = wrapLines(text, width)
		s.vp.SetXOffset(0)
		s.vp.SetHorizontalStep(0)
	} else {
		s.starts = nil
		s.vp.SetHorizontalStep(panStep)
	}
	// No taller than the content, so the footer sits right under it as on a
	// list — and, with no terminal height yet, so the viewport doesn't pad to
	// the "unlimited" height with a million blank lines.
	s.vp.Height = min(bodyHeight(s.sess), strings.Count(text, "\n")+1)
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
		// Wrap the whole line once at the hanging width, so no word is left
		// alone on a line; the first part just keeps the line's own indent.
		parts := strings.Split(ansi.Wrap(ansi.TruncateLeft(line, indent, ""), width-hang, ""), "\n")
		out = append(out, strings.Repeat(" ", indent)+parts[0])
		for _, p := range parts[1:] {
			out = append(out, strings.Repeat(" ", hang)+p)
		}
	}
	return strings.Join(out, "\n"), starts
}

func (s pagerScreen) crumb() string {
	if s.isRaw {
		return "raw" // the step before already names what this is
	}
	return s.title
}

func (s pagerScreen) View() string {
	head := titleStyle.Render(s.title) + "\n" + dimStyle.Render(truncate(s.about, s.sess.cols())) + "\n\n"
	switch s.state {
	case loading:
		return head + s.spinner.View() + " Loading…"
	case failed:
		return head + s.prob.view(s.sess.cols(), backKeys)
	}
	return head + s.vp.View() + "\n\n" + s.footer()
}

// footer is the pager's key help, or the save prompt while one is open.
func (s pagerScreen) footer() string {
	if s.save.active || s.save.notice != "" {
		return s.save.view()
	}
	// Pan and wrap only matter when a line is too wide for the view.
	keys := []string{"↑/↓ to scroll"}
	switch {
	case s.wrapped:
		keys = append(keys, "w unwrap")
	case s.cut:
		keys = append(keys, "←/→ pan", "w wrap")
	}
	switch {
	case s.isRaw:
		keys = append(keys, "r back")
	case s.rawPath != "":
		keys = append(keys, "r raw")
	}
	if s.raw != nil {
		keys = append(keys, "s save")
	}
	help := strings.Join(append(keys, "esc to go back"), " · ")
	if !(s.vp.AtTop() && s.vp.AtBottom()) {
		help = fmt.Sprintf("%.0f%% · ", s.vp.ScrollPercent()*100) + help
	}
	return dimStyle.Render(help)
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
