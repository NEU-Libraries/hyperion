package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"hyperion/internal/atlas"
	"hyperion/internal/export"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
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

// runTUI runs the interactive program. url/token may be empty — the TUI prompts
// for whatever is missing, so `./hyperion` works with no environment set up.
func runTUI(url, token string, concurrency int) int {
	if _, err := tea.NewProgram(newModel(url, token, concurrency)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "hyperion:", err)
		return 1
	}
	return 0
}

type state int

const (
	stateURL state = iota
	stateToken
	stateAuth
	stateType
	statePID
	stateOut
	stateResolving
	stateConfirm
	stateDownloading
	stateDone
	stateError
)

type failure struct {
	noid string
	err  error
}

type model struct {
	concurrency int

	// credentials — seeded from flags/env, prompted for when missing.
	url    string
	token  string
	client *atlas.Client
	user   atlas.User

	state state
	err   error

	urlInput   textinput.Model
	tokenInput textinput.Model
	pidInput   textinput.Model
	outInput   textinput.Model
	spinner    spinner.Model
	prog       progress.Model

	kindIdx   int // 0 = Collection, 1 = Set
	container atlas.Container
	outDir    string

	sub       chan export.Progress
	total     int
	completed int
	failures  []failure
}

type (
	authMsg struct {
		user   atlas.User
		client *atlas.Client
		err    error
	}
	resolvedMsg struct {
		container atlas.Container
		err       error
		kind      atlas.Kind
		pid       string
	}
	progressMsg     export.Progress
	downloadDoneMsg struct{}
)

func newModel(url, token string, concurrency int) model {
	urlIn := textinput.New()
	urlIn.Prompt = "Atlas URL: "
	urlIn.SetValue(defaultURL)
	urlIn.CursorEnd()

	tok := textinput.New()
	tok.Prompt = "Token: "
	tok.Placeholder = "paste your access token"
	tok.EchoMode = textinput.EchoPassword

	pid := textinput.New()
	pid.Prompt = "PID: "
	pid.Placeholder = "e.g. gqnkbq9"
	pid.CharLimit = 64

	out := textinput.New()
	out.Prompt = "Save to: "

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = dimStyle

	m := model{
		concurrency: concurrency, url: url, token: token,
		urlInput: urlIn, tokenInput: tok, pidInput: pid, outInput: out, spinner: sp,
		prog:  progress.New(progress.WithDefaultGradient()),
		state: firstState(url, token),
	}
	switch m.state {
	case stateURL:
		m.urlInput.Focus()
	case stateToken:
		m.tokenInput.Focus()
	}
	return m
}

// firstState skips the credential prompts for whatever was supplied via env/flag.
func firstState(url, token string) state {
	switch {
	case url == "":
		return stateURL
	case token == "":
		return stateToken
	default:
		return stateAuth
	}
}

func (m model) Init() tea.Cmd {
	if m.state == stateAuth {
		return tea.Batch(m.spinner.Tick, m.authCmd())
	}
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.prog.Width = min(msg.Width-4, 60)
		return m, nil
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		return m.handleKey(msg)
	case spinner.TickMsg:
		if m.state != stateAuth && m.state != stateResolving {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case authMsg:
		return m.handleAuth(msg)
	case resolvedMsg:
		return m.handleResolved(msg)
	case progressMsg:
		m.completed++
		if msg.Err != nil {
			m.failures = append(m.failures, failure{noid: msg.NOID, err: msg.Err})
		}
		return m, waitForProgress(m.sub)
	case downloadDoneMsg:
		m.state = stateDone
		return m, nil
	}
	return m.updateInputs(msg)
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateURL:
		switch msg.Type {
		case tea.KeyEnter:
			v := strings.TrimSpace(m.urlInput.Value())
			if v == "" {
				return m, nil
			}
			m.url = v
			m.urlInput.Blur()
			return m.afterURL()
		case tea.KeyEsc:
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.urlInput, cmd = m.urlInput.Update(msg)
		return m, cmd

	case stateToken:
		switch msg.Type {
		case tea.KeyEnter:
			v := strings.TrimSpace(m.tokenInput.Value())
			if v == "" {
				return m, nil
			}
			m.token = v
			m.tokenInput.Blur()
			m.state = stateAuth
			return m, tea.Batch(m.spinner.Tick, m.authCmd())
		case tea.KeyEsc:
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.tokenInput, cmd = m.tokenInput.Update(msg)
		return m, cmd

	case stateType:
		switch msg.String() {
		case "up", "k":
			m.kindIdx = max(m.kindIdx-1, 0)
		case "down", "j":
			m.kindIdx = min(m.kindIdx+1, 1)
		case "enter":
			m.pidInput.Focus()
			m.state = statePID
			return m, textinput.Blink
		case "q", "esc":
			return m, tea.Quit
		}
		return m, nil

	case statePID:
		switch msg.Type {
		case tea.KeyEnter:
			v := strings.TrimSpace(m.pidInput.Value())
			if v == "" {
				return m, nil
			}
			m.pidInput.SetValue(v)
			m.pidInput.Blur()
			m.outInput.SetValue("./" + v + "-mods")
			m.outInput.Focus()
			m.outInput.CursorEnd()
			m.state = stateOut
			return m, textinput.Blink
		case tea.KeyEsc:
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.pidInput, cmd = m.pidInput.Update(msg)
		return m, cmd

	case stateOut:
		switch msg.Type {
		case tea.KeyEnter:
			m.outDir = strings.TrimSpace(m.outInput.Value())
			if m.outDir == "" {
				return m, nil
			}
			m.outInput.Blur()
			m.state = stateResolving
			return m, tea.Batch(m.spinner.Tick, m.resolveCmd())
		case tea.KeyEsc:
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.outInput, cmd = m.outInput.Update(msg)
		return m, cmd

	case stateConfirm:
		switch msg.String() {
		case "y", "Y", "enter":
			return m.startDownload()
		default:
			return m, tea.Quit
		}

	case stateDone, stateError:
		return m, tea.Quit
	}
	return m, nil
}

// afterURL advances to the token prompt if the token is still missing, else auth.
func (m model) afterURL() (tea.Model, tea.Cmd) {
	if m.token == "" {
		m.tokenInput.Focus()
		m.state = stateToken
		return m, textinput.Blink
	}
	m.state = stateAuth
	return m, tea.Batch(m.spinner.Tick, m.authCmd())
}

func (m model) authCmd() tea.Cmd {
	client := atlas.New(m.url, m.token)
	return func() tea.Msg {
		u, err := client.Whoami(context.Background())
		return authMsg{user: u, client: client, err: err}
	}
}

func (m model) handleAuth(msg authMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = errors.New(authMessage(msg.err))
		m.state = stateError
		return m, nil
	}
	m.client = msg.client
	m.user = msg.user
	m.state = stateType
	return m, nil
}

func (m model) resolveCmd() tea.Cmd {
	kind := atlas.Collection
	if m.kindIdx == 1 {
		kind = atlas.Set
	}
	client, pid := m.client, m.pidInput.Value()
	return func() tea.Msg {
		c, err := resolve(context.Background(), client, kind, pid)
		return resolvedMsg{container: c, err: err, kind: kind, pid: pid}
	}
}

func (m model) handleResolved(msg resolvedMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.err != nil:
		m.err = errors.New(resolveMessage(msg.err, msg.kind, msg.pid))
		m.state = stateError
	case len(msg.container.Members) == 0:
		m.err = fmt.Errorf("that %s has no readable members to export", msg.kind)
		m.state = stateError
	default:
		m.container = msg.container
		m.total = len(msg.container.Members)
		m.state = stateConfirm
	}
	return m, nil
}

func (m model) startDownload() (tea.Model, tea.Cmd) {
	m.sub = make(chan export.Progress)
	client, members, dir, conc, sub := m.client, m.container.Members, m.outDir, m.concurrency, m.sub
	go func() {
		_, _ = export.Download(context.Background(), client, members, dir, conc, sub)
		close(sub)
	}()
	m.state = stateDownloading
	return m, waitForProgress(sub)
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

func (m model) updateInputs(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.state {
	case stateURL:
		m.urlInput, cmd = m.urlInput.Update(msg)
	case stateToken:
		m.tokenInput, cmd = m.tokenInput.Update(msg)
	case statePID:
		m.pidInput, cmd = m.pidInput.Update(msg)
	case stateOut:
		m.outInput, cmd = m.outInput.Update(msg)
	}
	return m, cmd
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("HYPERION") + dimStyle.Render("  bulk MODS exporter") + "\n")
	if m.user.NUID != "" {
		b.WriteString(okStyle.Render("✓ ") + fmt.Sprintf("%s (%s)\n", m.user.Name, m.user.NUID))
	}
	b.WriteString("\n")

	switch m.state {
	case stateURL:
		b.WriteString(m.urlInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to quit"))
	case stateToken:
		b.WriteString(dimStyle.Render(`From DRS → My DRS → "Programmatic access" → Generate token.`) + "\n" +
			m.tokenInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to quit"))
	case stateAuth:
		b.WriteString(m.spinner.View() + " Authenticating…")
	case stateType:
		b.WriteString(m.viewType())
	case statePID:
		b.WriteString(m.pidInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to quit"))
	case stateOut:
		b.WriteString(m.outInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to quit"))
	case stateResolving:
		b.WriteString(fmt.Sprintf("%s Resolving %s %s…", m.spinner.View(), m.kindLabel(), m.pidInput.Value()))
	case stateConfirm:
		b.WriteString(m.viewConfirm())
	case stateDownloading:
		b.WriteString(m.viewDownloading())
	case stateDone:
		b.WriteString(m.viewDone())
	case stateError:
		b.WriteString(errStyle.Render("✗ ") + m.err.Error() + "\n\n" + dimStyle.Render("press any key to quit"))
	}
	b.WriteString("\n")
	return b.String()
}

func (m model) viewType() string {
	var b strings.Builder
	b.WriteString("Export the MODS of every member of a…\n\n")
	for i, label := range []string{"Collection", "Set"} {
		if i == m.kindIdx {
			b.WriteString(selStyle.Render("› "+label) + "\n")
		} else {
			b.WriteString("  " + label + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ to move · enter to select · q to quit"))
	return b.String()
}

func (m model) viewConfirm() string {
	return fmt.Sprintf("%s %s\n%s\n\nDownload? %s",
		okStyle.Render("✓"), m.container.Title,
		dimStyle.Render(fmt.Sprintf("%d %s → %s", m.total, records(m.total), m.outDir)),
		dimStyle.Render("(y/n)"))
}

func (m model) viewDownloading() string {
	ratio := 0.0
	if m.total > 0 {
		ratio = float64(m.completed) / float64(m.total)
	}
	return fmt.Sprintf("Downloading MODS…\n\n%s\n%s",
		m.prog.ViewAs(ratio),
		dimStyle.Render(fmt.Sprintf("%d/%d", m.completed, m.total)))
}

func (m model) viewDone() string {
	saved := m.total - len(m.failures)
	var b strings.Builder
	b.WriteString(okStyle.Render(fmt.Sprintf("✓ Saved %d %s to %s", saved, records(saved), m.outDir)))
	if len(m.failures) > 0 {
		b.WriteString("\n" + warnStyle.Render(fmt.Sprintf("⚠ %d could not be exported:", len(m.failures))))
		for i, f := range m.failures {
			if i >= 10 {
				b.WriteString("\n" + warnStyle.Render(fmt.Sprintf("  …and %d more", len(m.failures)-10)))
				break
			}
			b.WriteString("\n" + warnStyle.Render(fmt.Sprintf("  %s: %v", f.noid, f.err)))
		}
	}
	b.WriteString("\n\n" + dimStyle.Render("press any key to quit"))
	return b.String()
}

func (m model) kindLabel() string {
	if m.kindIdx == 1 {
		return "set"
	}
	return "collection"
}

func records(n int) string {
	if n == 1 {
		return "record"
	}
	return "records"
}
