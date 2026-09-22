package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pidPromptScreen asks for the PID to inspect. It stays under the object
// screen it opens, so esc from the object comes back here with the PID kept.
type pidPromptScreen struct {
	sess  *session
	input textinput.Model
}

func newPIDPromptScreen(sess *session) pidPromptScreen {
	in := textinput.New()
	in.Prompt = "PID: "
	in.Placeholder = "any Work, Collection, Community, Set, FileSet, File, …"
	in.CharLimit = 64
	in.Focus()
	return pidPromptScreen{sess: sess, input: in}
}

func (s pidPromptScreen) Init() tea.Cmd { return textinput.Blink }

func (s pidPromptScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyEnter:
			v := strings.TrimSpace(s.input.Value())
			if v == "" {
				return s, nil
			}
			s.input.SetValue(v)
			return s, push(newObjectScreen(s.sess, v))
		case tea.KeyEsc:
			return s, pop
		}
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

func (s pidPromptScreen) View() string {
	return "Inspect an object\n\n" + s.input.View() + "\n\n" + dimStyle.Render("enter to inspect · esc to go back")
}

type objectState int

const (
	objectLoading objectState = iota
	objectLoaded
	objectError
)

// objectScreen shows one resolved object: its type, title, ancestor chain, and
// every field of its record in the order Atlas sent them.
type objectScreen struct {
	sess    *session
	pid     string
	state   objectState
	obj     atlas.Object
	err     error
	spinner spinner.Model
}

type objectMsg struct {
	pid string
	obj atlas.Object
	err error
}

func newObjectScreen(sess *session, pid string) objectScreen {
	return objectScreen{sess: sess, pid: pid, spinner: newSpinner()}
}

func (s objectScreen) Init() tea.Cmd {
	client, pid := s.sess.client, s.pid
	return tea.Batch(s.spinner.Tick, func() tea.Msg {
		obj, err := client.Inspect(context.Background(), pid)
		return objectMsg{pid: pid, obj: obj, err: err}
	})
}

func (s objectScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case objectMsg:
		if msg.pid != s.pid {
			return s, nil
		}
		if msg.err != nil {
			s.err = errors.New(inspectMessage(msg.err, s.pid))
			s.state = objectError
			return s, nil
		}
		s.obj = msg.obj
		s.state = objectLoaded
		return s, nil
	case spinner.TickMsg:
		if s.state != objectLoading {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyMsg:
		if s.state == objectError {
			return s, pop
		}
		switch msg.String() {
		case "esc", "q":
			return s, pop
		}
	}
	return s, nil
}

func (s objectScreen) View() string {
	switch s.state {
	case objectLoading:
		return s.spinner.View() + " Resolving PID " + s.pid + "…"
	case objectError:
		return errStyle.Render("✗ ") + s.err.Error() + "\n\n" + dimStyle.Render("press any key to go back")
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(string(s.obj.Type)) + dimStyle.Render("  PID ") + s.obj.NOID + "\n")
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(truncate(s.obj.Title, s.sess.width)) + "\n")
	if crumb := breadcrumb(s.obj.Ancestors); crumb != "" {
		b.WriteString(dimStyle.Render(truncate(crumb, s.sess.width)) + "\n")
	}
	b.WriteString("\n" + s.viewFields() + "\n")
	b.WriteString(dimStyle.Render("esc to go back"))
	return b.String()
}

// hiddenFields are shown above the field list (title, breadcrumb), not in it.
var hiddenFields = map[string]bool{"title": true, "ancestors": true}

func (s objectScreen) viewFields() string {
	labelWidth := 0
	for _, f := range s.obj.Fields {
		if !hiddenFields[f.Key] {
			labelWidth = max(labelWidth, lipgloss.Width(fieldLabel(f.Key)))
		}
	}
	valueWidth := 0
	if s.sess.width > 0 {
		valueWidth = max(s.sess.width-labelWidth-2, 10)
	}

	var b strings.Builder
	for _, f := range s.obj.Fields {
		if hiddenFields[f.Key] {
			continue
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("%-*s", labelWidth, fieldLabel(f.Key))) + "  " +
			fieldValue(f.Key, f.Value, valueWidth) + "\n")
	}
	return b.String()
}

// fieldLabels names the keys whose humanized form would mislead. Object ids
// read "PID" and person ids "NUID" — never "NOID", which is too easily
// confused with NUID.
var fieldLabels = map[string]string{
	"id":                       "PID",
	"valkyrie_id":              "Valkyrie ID",
	"nuid":                     "NUID",
	"depositor":                "Depositor (NUID)",
	"proxy_uploader":           "Proxy uploader (NUID)",
	"tombstoned_by":            "Tombstoned by (NUID)",
	"edit_users":               "Edit users (NUIDs)",
	"permanent_url":            "Permanent URL",
	"uri":                      "URI",
	"mime_type":                "MIME type",
	"orcid":                    "ORCID",
	"thumbnail_2x":             "Thumbnail (2x)",
	"personal_root_id":         "Personal root (PID)",
	"affiliated_community_ids": "Affiliated communities (PIDs)",
	"included_collections":     "Included collections (PIDs)",
	"included_works":           "Included works (PIDs)",
	"excluded_works":           "Excluded works (PIDs)",
}

func fieldLabel(key string) string {
	if l, ok := fieldLabels[key]; ok {
		return l
	}
	l := strings.ReplaceAll(key, "_", " ")
	return strings.ToUpper(l[:1]) + l[1:]
}

// fieldValue renders one raw JSON value on a single line of at most width
// columns (0 = unlimited): nulls and blanks dimmed, booleans as yes/no,
// string lists comma-joined, anything else as compact JSON.
func fieldValue(key string, raw json.RawMessage, width int) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return truncate(string(raw), width)
	}
	switch t := v.(type) {
	case nil:
		return dimStyle.Render("—")
	case bool:
		if !t {
			return "no"
		}
		if key == "tombstoned" {
			return warnStyle.Render("yes")
		}
		return "yes"
	case string:
		if strings.TrimSpace(t) == "" {
			return dimStyle.Render("—")
		}
		return truncate(t, width)
	case float64:
		if key == "size" {
			return truncate(humanBytes(t), width)
		}
		return truncate(string(raw), width)
	case []any:
		if len(t) == 0 {
			return dimStyle.Render("none")
		}
		strs := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				strs = nil
				break
			}
			strs = append(strs, s)
		}
		if strs != nil {
			return truncate(strings.Join(strs, ", "), width)
		}
	case map[string]any:
		if len(t) == 0 {
			return dimStyle.Render("none")
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		buf.Reset()
		buf.Write(raw)
	}
	return truncate(buf.String(), width)
}

// breadcrumb renders the ancestor chain root-first: "Title (pid) › …".
func breadcrumb(ancestors []atlas.Ancestor) string {
	parts := make([]string, len(ancestors))
	for i, a := range ancestors {
		t := a.Title
		if strings.TrimSpace(t) == "" {
			t = "(untitled " + strings.ToLower(a.Klass) + ")"
		}
		parts[i] = fmt.Sprintf("%s (%s)", t, a.NOID)
	}
	return strings.Join(parts, " › ")
}

func humanBytes(n float64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%.0f bytes", n)
	}
	div, exp := float64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB (%.0f bytes)", n/div, "KMGTPE"[exp], n)
}

// truncate flattens s to one line and cuts it to width columns with an
// ellipsis; width 0 means the terminal size isn't known yet, so no cut.
func truncate(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	r := []rune(s)
	r = r[:min(len(r), width)] // no rune is narrower than one column
	for len(r) > 0 && lipgloss.Width(string(r))+1 > width {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
