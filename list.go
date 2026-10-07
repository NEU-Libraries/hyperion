package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NEU-Libraries/hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tombstonedWarn flags a tombstoned object wherever it is listed or shown.
const tombstonedWarn = "tombstoned"

// listItem is one row of a list screen. Enter on a row runs open if set, else
// opens the object at pid; a row with neither is informational only.
type listItem struct {
	pid   string
	title string // shown as the row's name; the PID stands in until known
	kind  string // object type, e.g. "Work"
	note  string // secondary detail, dimmed
	warn  string // flagged detail, e.g. tombstonedWarn
	// lookup marks a row known only by PID: its title and type are resolved
	// when it first scrolls into view, so a long list costs requests only for
	// the rows a user actually looks at.
	lookup bool
	// inert marks a row that can't be opened (a system container): it renders
	// dimmed and enter ignores it.
	inert bool
	open  func(*session) screen
}

// listScreen shows a loaded list of objects to move through and open.
type listScreen struct {
	sess  *session
	id    int64
	title string
	about string
	empty string
	load  func(context.Context, *atlas.Client) (listResult, error)

	rawPath, rawName string // the Atlas route r shows raw ("" = none)
	rawTitle         string // what the raw response is ("" = the list's title)
	rawBody          []byte // its response, when already in hand

	state     loadState
	prob      problem
	spinner   spinner.Model
	items     []listItem
	cursor    int
	offset    int
	requested map[int]bool
}

// listResult is what a list's load reads: its rows, the about line if only
// the answer can say it ("100 of 312 matches"), and the route r shows if the
// answer decides it (a fallback route). An empty string keeps the screen's own.
type listResult struct {
	items   []listItem
	about   string
	rawPath string
}

type (
	listMsg struct {
		id int64
		listResult
		err error
	}
	lookupMsg struct {
		id  int64
		idx int
		obj atlas.Object
		err error
	}
)

func newListScreen(sess *session, title, about, empty string, load func(context.Context, *atlas.Client) ([]listItem, error)) listScreen {
	return newResultListScreen(sess, title, about, empty, func(ctx context.Context, c *atlas.Client) (listResult, error) {
		items, err := load(ctx, c)
		return listResult{items: items}, err
	})
}

// newResultListScreen is a list whose about line or raw route comes with its
// rows: a list that shows only part of what matched, or one read from
// whichever route answered.
func newResultListScreen(sess *session, title, about, empty string, load func(context.Context, *atlas.Client) (listResult, error)) listScreen {
	return listScreen{
		sess: sess, id: nextScreenID(), title: title, about: about, empty: empty, load: load,
		spinner: newSpinner(), requested: map[int]bool{},
	}
}

// staticList is a list whose rows need no request (beyond per-row lookups).
func staticList(sess *session, title, about, empty string, items []listItem) listScreen {
	return newListScreen(sess, title, about, empty, func(context.Context, *atlas.Client) ([]listItem, error) {
		return items, nil
	})
}

func (s listScreen) Init() tea.Cmd {
	id, client, load := s.id, s.sess.client, s.load
	return tea.Batch(s.spinner.Tick, func() tea.Msg {
		res, err := load(context.Background(), client)
		return listMsg{id: id, listResult: res, err: err}
	})
}

func (s listScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case listMsg:
		if msg.id != s.id {
			return s, nil
		}
		var n notice
		if errors.As(msg.err, &n) {
			s.empty, msg.items, msg.err = string(n), nil, nil
		}
		if msg.err != nil {
			s.prob = explain("Couldn't load "+s.title+".", msg.err)
			s.state = failed
			return s, nil
		}
		s.items = msg.items
		if msg.about != "" {
			s.about = msg.about
		}
		if msg.rawPath != "" {
			s.rawPath = msg.rawPath
		}
		s.state = loaded
		return s, s.lookups()
	case lookupMsg:
		if msg.id != s.id || msg.idx >= len(s.items) {
			return s, nil
		}
		s.items = append([]listItem(nil), s.items...)
		it := &s.items[msg.idx]
		it.lookup = false
		switch {
		case msg.err != nil:
			it.warn = briefError(msg.err)
		default:
			it.title, it.kind = msg.obj.Title, string(msg.obj.Type)
			if tombstoned(msg.obj) {
				it.warn = tombstonedWarn
			}
		}
		return s, nil
	case tea.WindowSizeMsg:
		s.scrollToCursor()
		return s, s.lookups()
	case spinner.TickMsg:
		if s.state != loading {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s listScreen) handleKey(msg tea.KeyMsg) (screen, tea.Cmd) {
	if s.state == failed {
		return s, problemKey(s.sess, s.prob, msg, pop)
	}
	page := bodyHeight(s.sess)
	switch msg.String() {
	case "esc":
		return s, pop
	case "r":
		if s.rawPath != "" {
			return s, push(newRawPager(s.sess, firstNonEmpty(s.rawTitle, s.title), s.rawPath, s.rawName, s.rawBody))
		}
		return s, nil
	case "up", "k":
		s.cursor--
	case "down", "j":
		s.cursor++
	case "pgup", "b":
		// A page moves the view a whole screen, the cursor with it — not just
		// the cursor, which would scroll the view by a single row.
		s.offset -= page
		s.cursor -= page
	case "pgdown", "f", " ":
		s.offset += page
		s.cursor += page
	case "home", "g":
		s.cursor = 0
	case "end", "G":
		s.cursor = len(s.items) - 1
	case "enter":
		if s.state != loaded || len(s.items) == 0 {
			return s, nil
		}
		it := s.items[s.cursor]
		switch {
		case it.inert:
		case it.open != nil:
			return s, push(it.open(s.sess))
		case it.pid != "":
			return s, push(newObjectScreen(s.sess, it.pid))
		}
		return s, nil
	default:
		return s, nil
	}
	s.cursor = max(min(s.cursor, len(s.items)-1), 0)
	s.scrollToCursor()
	return s, s.lookups()
}

// scrollToCursor moves the window just enough to keep the cursor in view.
func (s *listScreen) scrollToCursor() {
	h := bodyHeight(s.sess)
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+h {
		s.offset = s.cursor - h + 1
	}
	s.offset = max(min(s.offset, len(s.items)-h), 0)
}

// lookups starts a title/type lookup for each visible row that needs one.
func (s listScreen) lookups() tea.Cmd {
	if s.state != loaded {
		return nil
	}
	var cmds []tea.Cmd
	end := min(s.offset+bodyHeight(s.sess), len(s.items))
	for i := s.offset; i < end; i++ {
		if !s.items[i].lookup || s.requested[i] {
			continue
		}
		s.requested[i] = true // shared map: every copy of this screen agrees
		id, idx, pid, client := s.id, i, s.items[i].pid, s.sess.client
		cmds = append(cmds, func() tea.Msg {
			obj, err := client.Inspect(context.Background(), pid)
			return lookupMsg{id: id, idx: idx, obj: obj, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (s listScreen) crumb() string { return s.title }

func (s listScreen) View() string {
	title, cols := sectionTitle(s.title, s.about), frameWidth(s.sess)
	switch s.state {
	case loading:
		return frame(title, s.spinner.View()+" Loading…", cols, 0, true)
	case failed:
		return s.prob.panel(s.sess, title, backKeys)
	}
	if len(s.items) == 0 {
		help := "esc to go back"
		if s.rawPath != "" {
			help = "r raw · " + help
		}
		return frame(title, wrapped(dimStyle, s.empty, cols-frameCols), cols, 0, true) + "\n\n" + dimStyle.Render(help)
	}

	var b strings.Builder
	end := min(s.offset+bodyHeight(s.sess), len(s.items))
	for i := s.offset; i < end; i++ {
		b.WriteString(s.row(s.items[i], i == s.cursor) + "\n")
	}
	raw := ""
	if s.rawPath != "" {
		raw = "r raw · "
	}
	paging := ""
	if len(s.items) > bodyHeight(s.sess) {
		paging = "pgdn/pgup to page · "
	}
	help := fmt.Sprintf("%d/%d · ↑/↓ to move · %senter to open · %sesc to go back", s.cursor+1, len(s.items), paging, raw)
	return frame(title, b.String(), cols, 0, true) + "\n\n" + dimStyle.Render(help)
}

// row renders "› Title  Kind · pid · note  warn", cut to the terminal width.
func (s listScreen) row(it listItem, selected bool) string {
	name, detail := it.title, []string{}
	if it.kind != "" {
		detail = append(detail, it.kind)
	}
	if name == "" {
		name = it.pid
	} else if it.pid != "" {
		detail = append(detail, it.pid)
	}
	if it.note != "" {
		detail = append(detail, it.note)
	}

	width := max(s.sess.cols()-frameCols, 0) // inside the list's frame
	avail := func(used int) int {
		if width <= 0 {
			return 0
		}
		return max(width-used, 1)
	}
	prefix, style := "  ", lipgloss.NewStyle()
	if selected {
		prefix, style = "› ", selStyle
	}
	if it.inert {
		style = dimStyle
	}
	out := style.Render(prefix + truncate(name, avail(2)))
	used := lipgloss.Width(out)
	if len(detail) > 0 && (width <= 0 || width-used > 8) {
		d := dimStyle.Render("  " + truncate(strings.Join(detail, " · "), avail(used+2)))
		out += d
		used += lipgloss.Width(d)
	}
	if it.warn != "" && (width <= 0 || width-used > len(it.warn)+2) {
		out += "  " + warnStyle.Render(it.warn)
	}
	if it.lookup {
		out += dimStyle.Render("  …") // title still being looked up
	}
	return out
}

func tombstoned(obj atlas.Object) bool {
	for _, f := range obj.Fields {
		if f.Key == "tombstoned" {
			return string(f.Value) == "true"
		}
	}
	return false
}
