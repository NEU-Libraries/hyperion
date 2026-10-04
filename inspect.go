package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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

func (s pidPromptScreen) crumb() string { return "Inspect" }

func (s pidPromptScreen) typing() bool { return true }

func (s pidPromptScreen) View() string {
	return panel(s.sess, titleStyle.Render("Inspect an object"), s.input.View(), "enter to inspect · esc to go back")
}

// objectScreen shows one resolved object: its type, title, and ancestor
// chain, a menu of the sections that apply to its type, and as much of its
// record as fits below.
type objectScreen struct {
	sess     *session
	id       int64
	pid      string
	extra    []section
	state    loadState
	obj      atlas.Object
	sections []section
	sel      int
	prob     problem
	spinner  spinner.Model
}

type objectMsg struct {
	id  int64
	obj atlas.Object
	err error
}

// newObjectScreen opens pid. extra adds sections the caller already has the
// data for, after the type's own.
func newObjectScreen(sess *session, pid string, extra ...section) objectScreen {
	return objectScreen{sess: sess, id: nextScreenID(), pid: pid, extra: extra, spinner: newSpinner()}
}

func (s objectScreen) Init() tea.Cmd {
	id, client, pid := s.id, s.sess.client, s.pid
	return tea.Batch(s.spinner.Tick, func() tea.Msg {
		obj, err := client.Inspect(context.Background(), pid)
		return objectMsg{id: id, obj: obj, err: err}
	})
}

func (s objectScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case objectMsg:
		if msg.id != s.id {
			return s, nil
		}
		if msg.err != nil {
			s.prob = inspectProblem(s.pid, msg.err)
			s.state = failed
			return s, nil
		}
		s.obj = msg.obj
		s.sections = sectionsFor(msg.obj, s.extra)
		// Say up front what the account can't open, rather than as an error
		// after choosing it.
		for i, sec := range s.sections {
			switch {
			case sec.label == "MODS history" && !s.sess.user.ReadsMODSHistory(),
				sec.group == "Search" && !s.sess.user.ReadsIndex():
				s.sections[i].label += " (admins only)"
			}
		}
		s.state = loaded
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
		switch msg.String() {
		case "esc":
			return s, pop
		case "up", "k":
			s.sel = max(s.sel-1, 0)
		case "down", "j":
			s.sel = min(s.sel+1, len(s.sections)-1)
		case "enter":
			if s.state == loaded {
				return s, push(s.sections[s.sel].open(s.sess, s.obj))
			}
		case "r":
			if s.state == loaded {
				return s, push(newRawPager(s.sess, "Record", s.obj.Path, stem(s.obj, "record"), s.obj.Raw))
			}
		}
	}
	return s, nil
}

func (s objectScreen) View() string {
	switch s.state {
	case loading:
		return panel(s.sess, titleStyle.Render("PID "+s.pid), s.spinner.View()+" Looking it up…", "")
	case failed:
		return s.prob.panel(s.sess, titleStyle.Render("PID "+s.pid), backKeys)
	}
	cols := frameWidth(s.sess)

	// The object: its title and where it sits, framed under its type and PID.
	name := titleStyle.Render(string(s.obj.Type) + " " + s.obj.NOID)
	if tombstoned(s.obj) {
		name += "  " + warnStyle.Render("tombstoned")
	}
	about := lipgloss.NewStyle().Bold(true).Render(s.obj.Title)
	if crumb := breadcrumb(s.obj.Ancestors); crumb != "" {
		about += "\n" + dimStyle.Render(crumb)
	}
	head := frame(name, about, cols, 0, false)

	// Below it, the sections and the record preview share what's left above
	// the footer (2) and trailing newline: side by side when there's room,
	// stacked when there isn't. Either way the header and trail stay on screen.
	free := 1 << 20
	if s.sess.height > 0 {
		free = s.sess.height - headerLines - lipgloss.Height(head) - 3
	}
	menuWidth := s.menuWidth() + frameCols
	footer := dimStyle.Render("↑/↓ to move · enter to open · r raw · esc to go back")

	if cols >= menuWidth+40 { // side by side
		// As tall as the longer of the two — the menu or every previewable
		// field — and no taller than the screen allows.
		rows := max(len(s.sections), s.previewLen())
		if s.sess.height > 0 {
			rows = min(rows, free-frameRows)
		}
		menu, _ := s.viewMenu(max(rows, 3))
		previewWidth := cols - menuWidth - 1
		preview := s.viewFields(max(rows, 3), previewWidth-frameCols)
		panes := sideBySide(
			frame(titleStyle.Render("Sections"), menu, menuWidth, max(rows, 3)+frameRows, true),
			frame(dimStyle.Render("Record preview"), preview, previewWidth, max(rows, 3)+frameRows, false))
		return head + "\n" + panes + "\n\n" + footer
	}

	// Stacked: the menu first, as long as it needs (scrolling if it must);
	// the preview gets the rest, if that's enough to be useful.
	menu, menuRows := s.viewMenu(max(min(len(s.sections), free-frameRows), 3))
	out := head + "\n" + frame(titleStyle.Render("Sections"), menu, cols, 0, true)
	if room := free - menuRows - 2*frameRows; room >= 2 {
		height := room + frameRows
		if s.sess.height <= 0 {
			height = 0 // fit the fields, rather than pad to an unknown height
		}
		out += "\n" + frame(dimStyle.Render("Record preview"), s.viewFields(room, cols-frameCols), cols, height, false)
	}
	return out + "\n\n" + footer
}

// previewLen is how many record fields the preview can show.
func (s objectScreen) previewLen() int {
	n := 0
	for _, f := range s.obj.Fields {
		if !hiddenFields[f.Key] {
			n++
		}
	}
	return n
}

// menuWidth is how wide the section menu is: the group column, the cursor,
// and the longest section name.
func (s objectScreen) menuWidth() int {
	groupWidth, label := 0, 0
	for _, sec := range s.sections {
		groupWidth = max(groupWidth, len(sec.group))
		label = max(label, lipgloss.Width(sec.label))
	}
	return groupWidth + 2 + 2 + label
}

// crumb names the object once it's known, else the PID being resolved.
func (s objectScreen) crumb() string {
	if s.state == loaded {
		return string(s.obj.Type) + " " + s.obj.NOID
	}
	return "PID " + s.pid
}

// inspectProblem words a failed PID lookup for the object screen.
func inspectProblem(pid string, err error) problem {
	switch {
	case errors.Is(err, atlas.ErrNotFound):
		err = userErr("No object has this PID. Check it for typos.")
	case errors.Is(err, atlas.ErrForbidden):
		err = userErr("The object exists, but your account doesn't have permission to view it.")
	}
	return explain("Couldn't open PID "+pid+".", err)
}

// viewMenu renders the section menu in at most rows lines, with each group's
// name in a narrow left column where the group starts. If the menu is longer
// than rows it shows a window around the selection, marking what's above and
// below. It returns the menu and how many lines it used.
func (s objectScreen) viewMenu(rows int) (string, int) {
	groupWidth := 0
	for _, sec := range s.sections {
		groupWidth = max(groupWidth, len(sec.group))
	}
	from, to := 0, len(s.sections)
	if to > rows {
		// Leave a line for each "more" marker that will show.
		window := rows - 2
		from = max(min(s.sel-window/2, len(s.sections)-window), 0)
		to = from + window
	}

	var b strings.Builder
	lines := 0
	mark := func(text string) {
		b.WriteString(dimStyle.Render(strings.Repeat(" ", groupWidth+2)+text) + "\n")
		lines++
	}
	if from > 0 {
		mark("↑ more")
	}
	for i := from; i < to; i++ {
		sec := s.sections[i]
		group := ""
		if i == 0 || sec.group != s.sections[i-1].group || i == from {
			group = sec.group
		}
		row := dimStyle.Render(fmt.Sprintf("%-*s", groupWidth, group)) + "  "
		if i == s.sel {
			row += selStyle.Render("› " + sec.label)
		} else {
			row += "  " + sec.label
		}
		b.WriteString(row + "\n")
		lines++
	}
	if to < len(s.sections) {
		mark("↓ more")
	}
	return b.String(), lines
}

// technicalFields are kept for the end of the record preview: identifiers
// and image addresses a librarian rarely needs at a glance. The full Record
// shows every field in Atlas's order.
var technicalFields = map[string]bool{
	"valkyrie_id": true, "thumbnail": true, "thumbnail_2x": true, "preview": true,
	"derivative_permissions": true, "file_identifiers": true,
}

// hiddenFields are shown above the field list (title, breadcrumb), not in it.
var hiddenFields = map[string]bool{"title": true, "ancestors": true}

// viewFields previews the record in at most room lines, pointing at the
// Record section when it doesn't all fit.
func (s objectScreen) viewFields(room, width int) string {
	var shown, technical []atlas.Field
	labelWidth := 0
	for _, f := range s.obj.Fields {
		switch {
		case hiddenFields[f.Key]:
			continue
		case technicalFields[f.Key]:
			technical = append(technical, f)
		default:
			shown = append(shown, f)
		}
		labelWidth = max(labelWidth, lipgloss.Width(fieldLabel(f.Key)))
	}
	shown = append(shown, technical...)
	valueWidth := 0
	if width > 0 {
		valueWidth = max(width-labelWidth-2, 10)
	}

	cut := len(shown) > room
	if cut {
		shown = shown[:max(room-1, 0)]
	}
	var b strings.Builder
	for _, f := range shown {
		b.WriteString(dimStyle.Render(fmt.Sprintf("%-*s", labelWidth, fieldLabel(f.Key))) + "  " +
			fieldValue(f.Key, f.Value, valueWidth) + "\n")
	}
	if cut && room > 0 {
		b.WriteString(dimStyle.Render("… open Record for every field") + "\n")
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
	"personal_root":            "Is a personal root",
	"type":                     "Classification", // a FileSet's; Permissions' copy is dropped
	"created":                  "Created",
	"created_at":               "Created",
	"updated_at":               "Updated",
	"affiliated_community_ids": "Affiliated communities (PIDs)",
	"included_collections":     "Included collections (PIDs)",
	"included_works":           "Included works (PIDs)",
	"excluded_works":           "Excluded works (PIDs)",
	"version_id":               "Version ID",
	"actor_nuid":               "Actor (NUID)",
	"on_behalf_of_nuid":        "On behalf of (NUID)",
	"read_groups":              "Read groups",
}

func fieldLabel(key string) string {
	if l, ok := fieldLabels[key]; ok {
		return l
	}
	l := strings.ReplaceAll(key, "_", " ")
	return strings.ToUpper(l[:1]) + l[1:]
}

// humanTime shows an ISO-8601 timestamp as a person reads one, in local
// time: "15 Sep 2026, 4:14 pm EDT". The raw view keeps the exact value.
func humanTime(s string) (string, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", false
	}
	return t.Local().Format("2 Jan 2006, 3:04 pm MST"), true
}

// humanRole turns a machine role like "original_file" into "Original file".
func humanRole(role string) string {
	if role == "" {
		return ""
	}
	r := strings.ReplaceAll(role, "_", " ")
	return strings.ToUpper(r[:1]) + r[1:]
}

// fieldValue renders one raw JSON value on a single line of at most width
// columns (0 = unlimited): nulls and blanks dimmed, booleans as yes/no,
// string lists comma-joined, anything else as compact JSON.
func fieldValue(key string, raw json.RawMessage, width int) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return truncate(string(raw), width)
	}
	if key == "ancestors" {
		var chain []atlas.Ancestor
		if json.Unmarshal(raw, &chain) == nil {
			if len(chain) == 0 {
				return dimStyle.Render("none")
			}
			return truncate(breadcrumb(chain), width)
		}
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
		if when, ok := humanTime(t); ok {
			return truncate(when, width)
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
		// A list of {"id": …} objects (a File's identifiers) shows the ids.
		var ids []string
		for _, e := range t {
			m, ok := e.(map[string]any)
			id, isStr := m["id"].(string)
			if !ok || len(m) != 1 || !isStr {
				ids = nil
				break
			}
			ids = append(ids, id)
		}
		if ids != nil {
			return truncate(strings.Join(ids, ", "), width)
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

// breadcrumb renders the ancestor chain root-first: "Title (pid) › …", with
// a structural container labelled "Title (pid, system container)".
func breadcrumb(ancestors []atlas.Ancestor) string {
	parts := make([]string, len(ancestors))
	for i, a := range ancestors {
		t := a.Title
		if strings.TrimSpace(t) == "" {
			t = "(untitled " + strings.ToLower(a.Klass) + ")"
		}
		ref := a.NOID
		if label := structuralLabel(a); label != "" {
			ref += ", " + label
		}
		parts[i] = fmt.Sprintf("%s (%s)", t, ref)
	}
	return strings.Join(parts, " › ")
}

// structuralLabel names what a structural ancestor is ("" for content), so
// the Ancestors list and the breadcrumb agree.
func structuralLabel(a atlas.Ancestor) string {
	switch {
	case a.SystemContainer:
		return "system container"
	case a.PersonalRoot:
		return "personal root"
	}
	return ""
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
