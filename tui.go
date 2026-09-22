package main

import (
	"fmt"
	"os"
	"strings"

	"hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const defaultURL = "https://atlas.library.northeastern.edu"

var (
	titleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	selStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// runTUI runs the interactive program. url/token may be empty — the login
// screen prompts for whatever is missing, so `./hyperion` works with no
// environment set up.
//
// It runs on the alternate screen: each frame is drawn from the top-left
// corner rather than by moving the cursor up over the last one, so a line the
// terminal wrapped unexpectedly can't shift every later frame down a row.
func runTUI(url, token string, concurrency int) int {
	final, err := tea.NewProgram(newApp(url, token, concurrency), tea.WithAltScreen()).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hyperion:", err)
		return 1
	}
	// The alternate screen is gone on exit; leave any closing summary behind.
	if a, ok := final.(app); ok && a.sess.farewell != "" {
		fmt.Println(a.sess.farewell)
	}
	return 0
}

// session is the state every screen shares: credentials, the authenticated
// client and user, and the terminal size. Screens hold a pointer to it.
// cols is how many columns a screen may lay out in: one short of the
// terminal, because the app keeps every line clear of the last column (see
// app.View). 0 means the width isn't known yet.
func (s *session) cols() int {
	if s.width <= 1 {
		return 0
	}
	return s.width - 1
}

type session struct {
	url, token  string
	concurrency int

	client *atlas.Client
	user   atlas.User

	width, height int

	// wrap is whether pagers fold long lines; toggled with w, it carries over
	// to every pager for the rest of the session.
	wrap bool

	// farewell is printed to the terminal after the TUI exits — the export
	// summary, which would otherwise vanish with the alternate screen.
	farewell string
}

// screen is one page of the TUI. The app routes messages to the screen on top
// of its stack; a screen navigates by returning push/replace/pop commands.
// crumb is the screen's entry in the navigation trail ("" = not shown).
type screen interface {
	Init() tea.Cmd
	Update(tea.Msg) (screen, tea.Cmd)
	View() string
	crumb() string
}

type (
	pushMsg    struct{ s screen }
	replaceMsg struct{ s screen }
	popMsg     struct{}
)

// push opens s on top of the current screen; popping s comes back here.
func push(s screen) tea.Cmd { return func() tea.Msg { return pushMsg{s} } }

// replace swaps the current screen for s, so popping s skips the old one.
func replace(s screen) tea.Cmd { return func() tea.Msg { return replaceMsg{s} } }

// pop closes the current screen; popping the last screen quits.
func pop() tea.Msg { return popMsg{} }

// app is the root Bubble Tea model: a stack of screens under a common header.
type app struct {
	sess  *session
	stack []screen
}

func newApp(url, token string, concurrency int) app {
	sess := &session{url: url, token: token, concurrency: concurrency}
	return app{sess: sess, stack: []screen{newLoginScreen(sess)}}
}

func (a app) top() screen { return a.stack[len(a.stack)-1] }

// below returns a copy of the stack without its top, so updates never write
// into a backing array an earlier app value still shares.
func (a app) below() []screen { return append([]screen(nil), a.stack[:len(a.stack)-1]...) }

func (a app) Init() tea.Cmd { return a.top().Init() }

func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return a, tea.Quit
		}
	case tea.WindowSizeMsg:
		a.sess.width, a.sess.height = msg.Width, msg.Height
	case pushMsg:
		a.stack = append(a.below(), a.top(), msg.s)
		return a, msg.s.Init()
	case replaceMsg:
		a.stack = append(a.below(), msg.s)
		return a, msg.s.Init()
	case popMsg:
		if len(a.stack) <= 1 {
			return a, tea.Quit
		}
		a.stack = a.below()
		// The revealed screen missed any resize (and wrap toggle) while it was
		// covered; hand it the current size so it lays itself out again.
		if a.sess.width > 0 {
			s, cmd := a.top().Update(tea.WindowSizeMsg{Width: a.sess.width, Height: a.sess.height})
			a.stack = append(a.below(), s)
			return a, cmd
		}
		return a, nil
	}

	s, cmd := a.top().Update(msg)
	a.stack = append(a.below(), s)
	return a, cmd
}

// headerLines is how many lines the app header takes above every screen once
// signed in: the title, the user, the navigation trail, and a gap.
const headerLines = 4

func (a app) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("HYPERION") + dimStyle.Render("  inspect DRS objects · export MODS") + "\n")
	if a.sess.user.NUID != "" {
		b.WriteString(okStyle.Render("✓ ") + fmt.Sprintf("Signed in as %s (NUID %s)\n", a.sess.user.Name, a.sess.user.NUID))
		b.WriteString(a.trail() + "\n")
	}
	b.WriteString("\n" + a.top().View() + "\n")

	// Keep every line clear of the last column. A line that exactly fills the
	// terminal wraps if the terminal draws any glyph wider than Bubble Tea
	// measured it, and the wrap throws off the next redraw by a row — which
	// leaves a stale header line at the top of the screen.
	if a.sess.width > 1 {
		return lipgloss.NewStyle().MaxWidth(a.sess.width - 1).Render(b.String())
	}
	return b.String()
}

// trail is the path of screens to the current one — "Inspect › Work w1 ›
// FileSets" — with the current screen emphasised. When it doesn't fit, the
// oldest steps give way to "…".
func (a app) trail() string {
	var crumbs []string
	for _, s := range a.stack {
		if c := s.crumb(); c != "" {
			crumbs = append(crumbs, c)
		}
	}
	if len(crumbs) == 0 {
		return ""
	}
	render := func(cs []string, cut bool) string {
		parts := make([]string, len(cs))
		for i, c := range cs {
			if i == len(cs)-1 {
				parts[i] = c
			} else {
				parts[i] = dimStyle.Render(c)
			}
		}
		out := strings.Join(parts, dimStyle.Render(" › "))
		if cut {
			out = dimStyle.Render("… › ") + out
		}
		return out
	}
	out := render(crumbs, false)
	for cut := 1; a.sess.width > 1 && lipgloss.Width(out) > a.sess.width-1 && cut < len(crumbs); cut++ {
		out = render(crumbs[cut:], true)
	}
	return out
}

// menuView renders a vertical list with the selected row highlighted.
func menuView(labels []string, sel int) string {
	var b strings.Builder
	for i, label := range labels {
		if i == sel {
			b.WriteString(selStyle.Render("› "+label) + "\n")
		} else {
			b.WriteString("  " + label + "\n")
		}
	}
	return b.String()
}
