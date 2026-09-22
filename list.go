package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// listItem is one row of a list screen. Enter on a row runs open if set, else
// opens the object at pid; a row with neither is informational only.
type listItem struct {
	pid   string
	title string // shown as the row's name; the PID stands in until known
	kind  string // object type, e.g. "Work"
	note  string // secondary detail, dimmed
	warn  string // flagged detail, e.g. "tombstoned"
	// lookup marks a row known only by PID: its title and type are resolved
	// when it first scrolls into view, so a long list costs requests only for
	// the rows a user actually looks at.
	lookup bool
	open   func(*session) screen
}

// listScreen shows a loaded list of objects to move through and open.
type listScreen struct {
	sess  *session
	id    int64
	title string
	about string
	empty string
	load  func(context.Context, *atlas.Client) ([]listItem, error)

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

type (
	listMsg struct {
		id    int64
		items []listItem
		err   error
	}
	lookupMsg struct {
		id  int64
		idx int
		obj atlas.Object
		err error
	}
)

func newListScreen(sess *session, title, about, empty string, load func(context.Context, *atlas.Client) ([]listItem, error)) listScreen {
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
		items, err := load(context.Background(), client)
		return listMsg{id: id, items: items, err: err}
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
				it.warn = "tombstoned"
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
	case "esc", "q":
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
		s.cursor -= page
	case "pgdown", "f", " ":
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
		return frame(title, s.prob.view(cols-frameCols, backKeys), cols, 0, true)
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
	help := fmt.Sprintf("%d/%d · ↑/↓ to move · enter to open · %sesc to go back", s.cursor+1, len(s.items), raw)
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
