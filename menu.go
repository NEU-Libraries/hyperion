package main

import (
	tea "github.com/charmbracelet/bubbletea"
)

// menuItem is one entry on the main menu; open builds the screen it leads to.
type menuItem struct {
	label string
	open  func(*session) screen
}

// menuScreen is the root screen after login: pick what to do.
type menuScreen struct {
	sess  *session
	items []menuItem
	sel   int
}

func newMenuScreen(sess *session) menuScreen {
	return menuScreen{sess: sess, items: []menuItem{
		{label: "Inspect an object by PID", open: func(s *session) screen { return newPIDPromptScreen(s) }},
		{label: "Export MODS from a Collection or Set", open: func(s *session) screen { return newExportScreen(s) }},
	}}
}

func (s menuScreen) Init() tea.Cmd { return nil }

func (s menuScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nil
	}
	switch key.String() {
	case "up", "k":
		s.sel = max(s.sel-1, 0)
	case "down", "j":
		s.sel = min(s.sel+1, len(s.items)-1)
	case "enter":
		return s, push(s.items[s.sel].open(s.sess))
	case "q":
		// Not esc: everywhere else esc means back, so one press too many
		// would end the session.
		return s, tea.Quit
	}
	return s, nil
}

func (s menuScreen) crumb() string { return "" }

func (s menuScreen) View() string {
	labels := make([]string, len(s.items))
	for i, it := range s.items {
		labels[i] = it.label
	}
	return "What would you like to do?\n\n" + menuView(labels, s.sel) +
		"\n" + dimStyle.Render("↑/↓ to move · enter to select · q to quit")
}
