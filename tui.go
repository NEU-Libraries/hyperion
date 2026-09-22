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
func runTUI(url, token string, concurrency int) int {
	if _, err := tea.NewProgram(newApp(url, token, concurrency)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "hyperion:", err)
		return 1
	}
	return 0
}

// session is the state every screen shares: credentials, the authenticated
// client and user, and the terminal size. Screens hold a pointer to it.
type session struct {
	url, token  string
	concurrency int

	client *atlas.Client
	user   atlas.User

	width, height int
}

// screen is one page of the TUI. The app routes messages to the screen on top
// of its stack; a screen navigates by returning push/replace/pop commands.
type screen interface {
	Init() tea.Cmd
	Update(tea.Msg) (screen, tea.Cmd)
	View() string
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
		return a, nil
	}

	s, cmd := a.top().Update(msg)
	a.stack = append(a.below(), s)
	return a, cmd
}

func (a app) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("HYPERION") + dimStyle.Render("  bulk MODS exporter") + "\n")
	if a.sess.user.NUID != "" {
		b.WriteString(okStyle.Render("✓ ") + fmt.Sprintf("%s (%s)\n", a.sess.user.Name, a.sess.user.NUID))
	}
	b.WriteString("\n" + a.top().View() + "\n")
	return b.String()
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

func records(n int) string {
	if n == 1 {
		return "record"
	}
	return "records"
}
