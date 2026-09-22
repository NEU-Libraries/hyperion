package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Frames group a screen's content into titled panels:
//
//	╭─ Sections ───────────╮
//	│ › Record             │
//	╰──────────────────────╯
//
// The border is dim; the panel the keys act on is drawn in the accent colour,
// so there is always one clear place to look.

// frameChrome is what a frame adds around its content: a border and a space
// of padding on each side, a border line above and below.
const (
	frameCols = 4
	frameRows = 2
)

var (
	frameStyle  = dimStyle
	activeFrame = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
)

// frame draws body in a panel width columns wide, with title — styled by the
// caller — set into its top edge. height is the panel's full height including its border; 0 fits
// the body. Lines too long for the panel are cut with "…"; callers that want
// wrapping lay out to width-frameCols first.
func frame(title, body string, width, height int, active bool) string {
	border := frameStyle
	if active {
		border = activeFrame
	}
	inner := max(width-frameCols, 1)

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if height > 0 {
		rows := max(height-frameRows, 0)
		if len(lines) > rows {
			lines = lines[:rows]
		}
		for len(lines) < rows {
			lines = append(lines, "")
		}
	}

	var b strings.Builder
	title = ansi.Truncate(title, max(width-6, 1), "…")
	fill := max(width-lipgloss.Width(title)-5, 0)
	b.WriteString(border.Render("╭─ ") + title + border.Render(" "+strings.Repeat("─", fill)+"╮") + "\n")
	for _, l := range lines {
		l = ansi.Truncate(l, inner, "…")
		pad := strings.Repeat(" ", max(inner-lipgloss.Width(l), 0))
		b.WriteString(border.Render("│ ") + l + pad + border.Render(" │") + "\n")
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", max(width-2, 0)) + "╯"))
	return b.String()
}

// frameWidth is how wide a screen's frames are: every column the screen may
// use, or a typical width while the terminal's isn't known yet.
func frameWidth(sess *session) int {
	if c := sess.cols(); c > 0 {
		return c
	}
	return 100
}

// panel is a screen's usual shape: its content framed under title, full
// width, with the key help below the frame ("" for none).
func panel(sess *session, title, body, keys string) string {
	out := frame(title, body, frameWidth(sess), 0, true)
	if keys != "" {
		out += "\n\n" + dimStyle.Render(keys)
	}
	return out
}

// wrapEach wraps each line of text to width on its own, keeping its styling.
func wrapEach(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = wrapped(lipgloss.NewStyle(), l, width)
	}
	return strings.Join(lines, "\n")
}

// sideBySide joins two panels into one row, a space apart.
func sideBySide(left, right string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

// sectionTitle is a section frame's title: the section, then the object it
// belongs to, dimmed — "MODS  Work p5hqf0q · Sample Audio Recording".
func sectionTitle(title, about string) string {
	if about == "" {
		return titleStyle.Render(title)
	}
	return titleStyle.Render(title) + "  " + dimStyle.Render(about)
}
