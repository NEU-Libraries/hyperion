package main

import (
	"context"
	"strings"

	"hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type loginState int

const (
	loginURL loginState = iota
	loginToken
	loginAuth
	loginError
)

// loginScreen collects whatever credentials the environment didn't supply,
// proves them against GET /user, and hands off to the main menu.
type loginScreen struct {
	sess  *session
	state loginState
	prob  problem

	urlInput   textinput.Model
	tokenInput textinput.Model
	spinner    spinner.Model
}

type authMsg struct {
	user   atlas.User
	client *atlas.Client
	err    error
}

func newLoginScreen(sess *session) loginScreen {
	urlIn := textinput.New()
	urlIn.Prompt = "Atlas URL: "
	urlIn.SetValue(defaultURL)
	urlIn.CursorEnd()

	tok := textinput.New()
	tok.Prompt = "Token: "
	tok.Placeholder = "paste your access token"
	tok.EchoMode = textinput.EchoPassword

	s := loginScreen{
		sess: sess, urlInput: urlIn, tokenInput: tok, spinner: newSpinner(),
		state: firstLoginState(sess.url, sess.token),
	}
	switch s.state {
	case loginURL:
		s.urlInput.Focus()
	case loginToken:
		s.tokenInput.Focus()
	}
	return s
}

// firstLoginState skips the prompts for whatever was supplied via env/flag.
func firstLoginState(url, token string) loginState {
	switch {
	case url == "":
		return loginURL
	case token == "":
		return loginToken
	default:
		return loginAuth
	}
}

func (s loginScreen) Init() tea.Cmd {
	if s.state == loginAuth {
		return tea.Batch(s.spinner.Tick, s.authCmd())
	}
	return textinput.Blink
}

func (s loginScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return s.handleKey(msg)
	case spinner.TickMsg:
		if s.state != loginAuth {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case authMsg:
		if msg.err != nil {
			s.prob = explain("Couldn't sign in to Atlas.", msg.err)
			s.state = loginError
			return s, nil
		}
		s.sess.client = msg.client
		s.sess.user = msg.user
		return s, replace(newMenuScreen(s.sess))
	}

	var cmd tea.Cmd
	switch s.state {
	case loginURL:
		s.urlInput, cmd = s.urlInput.Update(msg)
	case loginToken:
		s.tokenInput, cmd = s.tokenInput.Update(msg)
	}
	return s, cmd
}

func (s loginScreen) handleKey(msg tea.KeyMsg) (screen, tea.Cmd) {
	switch s.state {
	case loginURL:
		switch msg.Type {
		case tea.KeyEnter:
			v := strings.TrimSpace(s.urlInput.Value())
			if v == "" {
				return s, nil
			}
			s.sess.url = v
			s.urlInput.Blur()
			if s.sess.token == "" {
				s.tokenInput.Focus()
				s.state = loginToken
				return s, textinput.Blink
			}
			s.state = loginAuth
			return s, tea.Batch(s.spinner.Tick, s.authCmd())
		case tea.KeyEsc:
			return s, tea.Quit
		}
		var cmd tea.Cmd
		s.urlInput, cmd = s.urlInput.Update(msg)
		return s, cmd

	case loginToken:
		switch msg.Type {
		case tea.KeyEnter:
			v := strings.TrimSpace(s.tokenInput.Value())
			if v == "" {
				return s, nil
			}
			s.sess.token = v
			s.tokenInput.Blur()
			s.state = loginAuth
			return s, tea.Batch(s.spinner.Tick, s.authCmd())
		case tea.KeyEsc:
			return s, tea.Quit
		}
		var cmd tea.Cmd
		s.tokenInput, cmd = s.tokenInput.Update(msg)
		return s, cmd

	case loginError:
		return s, tea.Quit
	}
	return s, nil
}

func (s loginScreen) authCmd() tea.Cmd {
	client := atlas.New(s.sess.url, s.sess.token)
	return func() tea.Msg {
		u, err := client.Whoami(context.Background())
		return authMsg{user: u, client: client, err: err}
	}
}

func (s loginScreen) View() string {
	switch s.state {
	case loginURL:
		return s.urlInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to quit")
	case loginToken:
		return dimStyle.Render(`From DRS → My DRS → "Programmatic access" → Generate token.`) + "\n" +
			s.tokenInput.View() + "\n\n" + dimStyle.Render("enter to continue · esc to quit")
	case loginAuth:
		return s.spinner.View() + " Authenticating…"
	default:
		return s.prob.view(s.sess.width, "quit")
	}
}

func (s loginScreen) crumb() string { return "" }

func newSpinner() spinner.Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = dimStyle
	return sp
}
