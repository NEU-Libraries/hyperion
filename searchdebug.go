package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// searchDebugScreen tests words against one Work, Collection or Community:
// type them, and it shows, field by field, where Solr matches them and how
// it scores the object. It asks Atlas a moment after typing stops.
type searchDebugScreen struct {
	sess    *session
	id      int64
	obj     atlas.Object
	input   textinput.Model
	spinner spinner.Model

	seq    int    // counts edits; a debounce or answer for an older one is dropped
	asked  string // the words last sent to Atlas
	busy   bool
	cancel context.CancelFunc

	res     *reading // the last answer, kept on screen while the next loads
	trouble string   // why there is no answer, already styled
	sel     string   // the selected field
	explain bool     // the detail panel shows Solr's explanation tree
	scroll  int      // the detail panel's first line
}

// searchDebounce is how long typing must pause before Hyperion asks Atlas.
const searchDebounce = 300 * time.Millisecond

type (
	debounceMsg struct {
		id  int64
		seq int
	}
	explainMsg struct {
		id  int64
		seq int
		ex  atlas.SearchExplanation
		err error
	}
)

func searchDebugSection(sess *session, obj atlas.Object) screen {
	in := textinput.New()
	in.Prompt = "Words: "
	in.Placeholder = "words to test against this " + string(obj.Type)
	in.CharLimit = 200
	in.Focus()
	return searchDebugScreen{sess: sess, id: nextScreenID(), obj: obj, input: in, spinner: newSpinner()}
}

func (s searchDebugScreen) Init() tea.Cmd { return textinput.Blink }

func (s searchDebugScreen) crumb() string { return "Search debug" }

func (s searchDebugScreen) typing() bool { return true }

func (s searchDebugScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case debounceMsg:
		if msg.id != s.id || msg.seq != s.seq {
			return s, nil
		}
		if !s.busy && strings.TrimSpace(s.input.Value()) == s.asked {
			return s, nil // an edit that came back to the words already answered
		}
		return s.ask()
	case explainMsg:
		if msg.id != s.id || msg.seq != s.seq {
			return s, nil // typing has moved on; a newer answer is coming
		}
		s.busy, s.cancel = false, nil
		s.take(msg.ex, msg.err)
		return s, nil
	case spinner.TickMsg:
		if !s.busy {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyMsg:
		return s.key(msg)
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

func (s searchDebugScreen) key(msg tea.KeyMsg) (screen, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if s.explain {
			s.explain, s.scroll = false, 0
			return s, nil
		}
		if s.cancel != nil {
			s.cancel()
		}
		return s, pop
	case tea.KeyEnter:
		// Words not yet sent go now; otherwise enter swaps the detail panel.
		if q := strings.TrimSpace(s.input.Value()); q != "" && q != s.asked {
			return s.ask()
		}
		if s.res != nil {
			s.explain, s.scroll = !s.explain, 0
		}
		return s, nil
	case tea.KeyUp, tea.KeyDown:
		if s.res != nil {
			order := s.fieldOrder()
			i := slices.IndexFunc(order, func(f fieldReading) bool { return f.Field == s.sel })
			if msg.Type == tea.KeyDown {
				i = (i + 1) % len(order)
			} else {
				i = (i - 1 + len(order)) % len(order)
			}
			s.sel, s.explain, s.scroll = order[i].Field, false, 0
		}
		return s, nil
	case tea.KeyPgDown, tea.KeyPgUp:
		if s.res != nil {
			_, rows := s.rows()
			step := max(rows-1, 1)
			if msg.Type == tea.KeyPgUp {
				step = -step
			}
			s.scroll = max(min(s.scroll+step, s.detailLen()-rows), 0)
		}
		return s, nil
	}
	before := s.input.Value()
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	if s.input.Value() == before {
		return s, cmd
	}
	s.seq++
	id, seq := s.id, s.seq
	return s, tea.Batch(cmd, tea.Tick(searchDebounce, func(time.Time) tea.Msg { return debounceMsg{id: id, seq: seq} }))
}

// ask sends the words to Atlas, cancelling any request still out. Blank
// words clear the screen instead: a blank search browses, and every object
// scores the same.
func (s searchDebugScreen) ask() (screen, tea.Cmd) {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	q := strings.TrimSpace(s.input.Value())
	s.asked = q
	if q == "" {
		s.busy, s.res, s.trouble = false, nil, ""
		return s, nil
	}
	s.seq++
	ctx, cancel := context.WithCancel(context.Background())
	id, seq, client, noid := s.id, s.seq, s.sess.client, s.obj.NOID
	s.busy, s.cancel = true, cancel
	return s, tea.Batch(s.spinner.Tick, func() tea.Msg {
		ex, err := client.ExplainSearch(ctx, noid, q)
		return explainMsg{id: id, seq: seq, ex: ex, err: err}
	})
}

// take shows Atlas's answer, keeping the selected field where it still
// scores, else moving to the strongest.
func (s *searchDebugScreen) take(ex atlas.SearchExplanation, err error) {
	var he *atlas.HTTPError
	switch {
	case err == nil:
	case errors.Is(err, atlas.ErrForbidden):
		s.res, s.trouble = nil, dimStyle.Render(indexLimit)
		return
	case errors.Is(err, atlas.ErrNotIndexed):
		s.res, s.trouble = nil, errStyle.Render("✗ ")+string(notIndexed(s.obj.Type))
		return
	case errors.As(err, &he) && he.Status == 502:
		s.res, s.trouble = nil, errStyle.Render("✗ ")+"Atlas couldn't reach Solr. Try again in a few minutes."
		return
	default:
		s.res, s.trouble = nil, explain("Couldn't explain the search.", err).body(s.innerWidth())
		return
	}
	r := readExplanation(ex)
	s.res, s.trouble = &r, ""
	order := s.fieldOrder()
	keep := slices.IndexFunc(order, func(f fieldReading) bool { return f.Field == s.sel && f.points() > 0 })
	if keep < 0 && len(order) > 0 && (order[0].points() > 0 || s.sel == "") {
		s.sel, s.scroll = order[0].Field, 0
	}
}

// fieldOrder is the fields strongest first: those that scored, then those
// with text that didn't match, then those with none to show. Ties keep qf's
// order.
func (s searchDebugScreen) fieldOrder() []fieldReading {
	if s.res == nil {
		return nil
	}
	rank := func(f fieldReading) int {
		switch {
		case f.points() > 0:
			return 0
		case f.hasText() || f.Reason == "not_stored":
			return 1
		}
		return 2
	}
	order := slices.Clone(s.res.fields)
	slices.SortStableFunc(order, func(a, b fieldReading) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		return cmp.Compare(b.points(), a.points())
	})
	return order
}

func (s searchDebugScreen) selected() fieldReading {
	for _, f := range s.res.fields {
		if f.Field == s.sel {
			return f
		}
	}
	return s.res.fields[0]
}

// --- Layout ---

func (s searchDebugScreen) innerWidth() int { return frameWidth(s.sess) - frameCols }

// fieldsWidth is the field list's width, frame included: its widest row,
// with the words that scored cut at 30 columns.
func (s searchDebugScreen) fieldsWidth() int {
	name, words := 0, len("analysis failed")
	for _, f := range s.res.fields {
		name = max(name, len(f.Field))
		words = max(words, lipgloss.Width(strings.Join(scoredClauses(f, s.res), " ")))
	}
	return 2 + name + 6 + 7 + 2 + min(words, 30) + frameCols
}

// sideBySide reports whether the field list and the detail sit side by side.
func (s searchDebugScreen) split() bool {
	return frameWidth(s.sess) >= s.fieldsWidth()+40
}

// rows is how many lines the field list and the detail panel may show:
// what the terminal has left under the app header, the words and the
// verdict, above the key row. Side by side they share one height, as tall as
// the longer needs; stacked, the field list takes up to half. With no
// terminal height yet, each is as long as its content.
func (s searchDebugScreen) rows() (fields, detail int) {
	n, content := len(s.res.fields), s.detailLen()
	if s.sess.height <= 0 {
		if s.split() {
			return max(n, content), max(n, content)
		}
		return n, content
	}
	// The header, a spare line, and the blank and key rows below.
	free := s.sess.height - headerLines - 1 - lipgloss.Height(s.topPanel()) - 2
	if s.split() {
		rows := max(min(max(n, content), free-frameRows), 3)
		return rows, rows
	}
	free -= 2 * frameRows
	fields = max(min(n, free/2), 3)
	return fields, max(min(content, free-fields), 3)
}

func (s searchDebugScreen) View() string {
	out := s.topPanel()
	if s.res != nil {
		order := s.fieldOrder()
		title := titleStyle.Render("Fields") + "  " + dimStyle.Render("qf · strongest first")
		fieldRows, detailRows := s.rows()
		if s.split() {
			fields := frame(title, s.fieldRows(order, fieldRows), s.fieldsWidth(), fieldRows+frameRows, !s.explain)
			width := frameWidth(s.sess) - s.fieldsWidth() - 1
			out += "\n" + sideBySide(fields, s.detailPanel(width, detailRows))
		} else {
			fields := frame(title, s.fieldRows(order, fieldRows), frameWidth(s.sess), fieldRows+frameRows, !s.explain)
			out += "\n" + fields + "\n" + s.detailPanel(frameWidth(s.sess), detailRows)
		}
	}
	return out + "\n\n" + dimStyle.Render(s.keys())
}

// keys is the key row. Typing needs no key of its own: the cursor sits in
// the words.
func (s searchDebugScreen) keys() string {
	switch {
	case s.res == nil:
		return "esc to go back"
	case s.explain:
		return "pgdn/pgup to page · enter fields · esc fields"
	}
	return "↑/↓ field · pgdn/pgup to page · enter explanation · esc to go back"
}

// topPanel holds the words being typed and the verdict on them.
func (s searchDebugScreen) topPanel() string {
	title := sectionTitle("Search debug", objectContext(s.obj))
	in := s.input
	in.Width = max(s.innerWidth()-lipgloss.Width(in.Prompt)-1, 10)
	body := in.View() + "\n\n" + s.verdict()
	return frame(title, body, frameWidth(s.sess), 0, true)
}

// verdict says whether the object matches, and how each word fared.
func (s searchDebugScreen) verdict() string {
	width := s.innerWidth()
	var lines []string
	if s.busy {
		lines = append(lines, strings.TrimRight(s.spinner.View(), " ")+dimStyle.Render(" asking Atlas…"))
	}
	switch {
	case s.trouble != "":
		return strings.Join(append(lines, wrapped(lipgloss.NewStyle(), s.trouble, width)), "\n")
	case s.res == nil:
		if !s.busy {
			lines = append(lines, dimStyle.Render("Type words to see how Solr scores this "+string(s.obj.Type)+" for them."))
		}
		return strings.Join(lines, "\n")
	}
	r := s.res
	if r.ex.Matched {
		line := okStyle.Render("✓ Matches") + dimStyle.Render(" · score ") + lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%.2f", r.ex.Score))
		var hidden []string
		for _, h := range r.ex.HiddenBy {
			hidden = append(hidden, firstNonEmpty(hiddenWords[h], h))
		}
		if len(hidden) > 0 {
			line += warnStyle.Render(" · but catalog search never shows it: " + strings.Join(hidden, ", "))
		} else {
			line += dimStyle.Render(" · no catalog filter hides it")
		}
		lines = append(lines, line)
	} else {
		lines = append(lines, errStyle.Render("✗ No match")+
			dimStyle.Render(fmt.Sprintf(" · needs %d of %d, %d found", r.required, len(r.clauses), r.found)))
	}

	pad := 0
	for _, c := range append(slices.Clone(r.clauses), r.bonuses...) {
		pad = max(pad, lipgloss.Width(clauseName(c)))
	}
	pad = min(pad+2, 28)
	for _, c := range r.clauses {
		lines = append(lines, "  "+clauseLine(c, pad, r.fields))
	}
	for _, c := range r.bonuses {
		lines = append(lines, "  "+clauseLine(c, pad, r.fields))
	}

	rule := fmt.Sprintf("  %d of %d required", r.required, len(r.clauses))
	if mm := r.ex.Handler.MM; mm != nil {
		rule = "  minimum match " + *mm + " → " + strings.TrimSpace(rule)
	}
	if tie := r.ex.Handler.Tie; tie != nil {
		rule += fmt.Sprintf(" · tie %g", *tie)
	}
	if r.adjust != 1 {
		rule += fmt.Sprintf(" · boost function ×%.2f", r.adjust)
	}
	lines = append(lines, dimStyle.Render(rule))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(lines, "\n")
}

// clauseName is how a clause is named in the verdict and the detail.
func clauseName(c clause) string {
	switch {
	case c.bonus:
		return "phrase bonus"
	case c.phrase:
		return `"` + c.text + `"`
	}
	return c.text
}

func clauseLine(c clause, pad int, fields []fieldReading) string {
	name := lipgloss.NewStyle().Bold(true).Render(clauseName(c))
	name += strings.Repeat(" ", max(pad-lipgloss.Width(clauseName(c)), 1))
	best, ok := c.best()
	if !ok {
		line := name + warnStyle.Render("in no field")
		if near := nearestWord(c.text, fields); near != "" && !c.phrase {
			line += dimStyle.Render(" · nearest indexed word: ") + synString.Render(near)
		}
		return line
	}
	line := name + dimStyle.Render("best in ") + titleStyle.UnsetBold().Render(best.field) + dimStyle.Render(fmt.Sprintf(" %.2f", best.points))
	if c.bonus {
		line += dimStyle.Render(fmt.Sprintf(` · "%s"`, c.text))
	}
	if others := len(c.hits) - 1; others > 0 {
		line += dimStyle.Render(fmt.Sprintf(" · +1%% from %s", plural(others, "other field")))
	}
	return line
}

// fieldRows lists the fields with their boost, points, and the words that
// scored in each, in at most rows lines: a window around the selected field
// when they don't all fit, marking what's above and below.
func (s searchDebugScreen) fieldRows(order []fieldReading, rows int) string {
	name := 0
	for _, f := range order {
		name = max(name, len(f.Field))
	}
	from, to := 0, len(order)
	if to > rows {
		window := max(rows-2, 1)
		sel := slices.IndexFunc(order, func(f fieldReading) bool { return f.Field == s.sel })
		from = max(min(sel-window/2, len(order)-window), 0)
		to = from + window
	}
	var b strings.Builder
	if from > 0 {
		b.WriteString(dimStyle.Render("  ↑ more") + "\n")
	}
	for _, f := range order[from:to] {
		mark, label := "  ", fmt.Sprintf("%-*s", name, f.Field)
		if f.Field == s.sel {
			mark, label = selStyle.Render("› "), selStyle.Render(label)
		}
		boost := dimStyle.Render(fmt.Sprintf(" ×%-4s", trimFloat(f.boost)))
		var pts, words string
		switch {
		case f.points() > 0:
			pts = fmt.Sprintf("%7.2f", f.points())
			words = synString.Render(strings.Join(scoredClauses(f, s.res), " "))
		case f.Reason == "not_stored":
			pts, words = dimStyle.Render(fmt.Sprintf("%7s", "—")), dimStyle.Render("not stored")
		case f.Reason == "analysis_failed":
			pts, words = dimStyle.Render(fmt.Sprintf("%7s", "—")), warnStyle.Render("analysis failed")
		case f.hasText():
			pts, words = dimStyle.Render(fmt.Sprintf("%7s", "—")), dimStyle.Render("no match")
		default:
			pts, words = dimStyle.Render(fmt.Sprintf("%7s", "·")), dimStyle.Render("no value")
		}
		b.WriteString(mark + label + boost + pts + "  " + words + "\n")
	}
	if to < len(order) {
		b.WriteString(dimStyle.Render("  ↓ more") + "\n")
	}
	return b.String()
}

// scoredClauses names the clauses that scored in a field, in typed order.
func scoredClauses(f fieldReading, r *reading) []string {
	var names []string
	for _, c := range append(slices.Clone(r.clauses), r.bonuses...) {
		if _, ok := f.scores[c.text]; ok {
			names = append(names, clauseName(c))
		}
	}
	return names
}

func trimFloat(f float64) string { return strings.TrimSuffix(fmt.Sprintf("%g", f), ".0") }

// detailPanel frames the selected field's detail, or the explanation tree,
// rows lines tall, from the scroll position.
func (s searchDebugScreen) detailPanel(width, rows int) string {
	var title string
	var lines []string
	if s.explain {
		title = titleStyle.Render("Solr explanation") + "  " + dimStyle.Render("enter for fields")
		lines = s.explainLines(width - frameCols)
	} else {
		f := s.selected()
		stored := "not stored"
		if f.Stored {
			stored = "stored"
		}
		title = titleStyle.Render(f.Field) + "  " + dimStyle.Render(fmt.Sprintf("×%s · %s", trimFloat(f.boost), stored))
		lines = s.fieldLines(f, width-frameCols)
	}
	from := min(s.scroll, max(len(lines)-rows, 0))
	to := min(from+rows, len(lines))
	shown := lines[from:to]
	if from > 0 && len(shown) > 0 {
		shown[0] = dimStyle.Render("↑ more")
	}
	if to < len(lines) && len(shown) > 0 {
		shown[len(shown)-1] = dimStyle.Render("↓ more")
	}
	return frame(title, strings.Join(shown, "\n"), width, rows+frameRows, s.explain)
}

// detailLen is how many lines the detail panel's content takes.
func (s searchDebugScreen) detailLen() int {
	width := frameWidth(s.sess) - frameCols
	if s.split() {
		width -= s.fieldsWidth() + 1
	}
	if s.explain {
		return len(s.explainLines(width))
	}
	return len(s.fieldLines(s.selected(), width))
}

// fieldLabels name the catalog's searched fields as a librarian knows them.
var searchFieldLabels = map[string]string{
	"title_tsim":                 "Title",
	"title_stem_tesim":           "Title, plurals made singular",
	"title_kstem_tesim":          "Title, other forms of each word",
	"title_plain_tsim":           "Title without formatting",
	"title_split_tesim":          "Title, split into words",
	"title_variant_tesim":        "Other titles",
	"description_tsim":           "Description",
	"description_stem_tesim":     "Description, plurals made singular",
	"description_kstem_tesim":    "Description, other forms of each word",
	"alternate_ids_tsim":         "PID",
	"identifier_tesim":           "Identifiers",
	"subject_title_tesim":        "Subject titles",
	"contents_tesim":             "Contents",
	"descriptive_keywords_tesim": "Keywords: subjects, names, genre, publisher and place",
	"name_variant_teim":          "Other forms of the creators' names",
	"full_text_tesimv":           "Full text",
}

// reasonWords explains a field with no text to show.
func reasonWords(f fieldReading, t atlas.Type) string {
	switch f.Reason {
	case "not_stored":
		if f.Field == "name_variant_teim" {
			return "Not stored. Atlas fills this field with other forms of each creator's name, so Solr can't show its words."
		}
		return "Not stored, and nothing stored is copied into it, so Solr can't show its words."
	case "full_text":
		return "Full text is too long to analyse. When the " + string(t) + " matches, Solr's highlighter shows up to 3 passages."
	case "analysis_failed":
		return "Solr couldn't analyse this field's text, so its words can't be shown."
	case "not_in_schema":
		return "The search handler searches this field, but Solr's schema has no such field."
	}
	return "No value on this " + string(t) + "."
}

// hitStyle marks a word that matched as typed; stemStyle one that matched
// only once Solr changed its form.
var (
	hitStyle  = lipgloss.NewStyle().Background(lipgloss.Color("214")).Foreground(lipgloss.Color("16")).Bold(true)
	stemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true).Underline(true)
)

// fieldLines is the detail for one field: its text with the matched words
// marked, its index and query tokens, and what each word scored in it.
func (s searchDebugScreen) fieldLines(f fieldReading, width int) []string {
	r := s.res
	var out []string
	add := func(text string) {
		out = append(out, strings.Split(wrapped(lipgloss.NewStyle(), text, width), "\n")...)
	}

	out = append(out, lipgloss.NewStyle().Bold(true).Render(firstNonEmpty(searchFieldLabels[f.Field], f.Field)))
	var sources []string
	for _, v := range f.Values {
		if !slices.Contains(sources, v.Source) {
			sources = append(sources, v.Source)
		}
	}
	copied := len(sources) > 1 || len(sources) == 1 && sources[0] != f.Field
	if copied {
		add(dimStyle.Render("Copied from ") + synElement.Render(strings.Join(sources, ", ")))
	}
	out = append(out, "")

	typed := map[string]bool{}
	for _, c := range r.clauses {
		for _, w := range strings.Fields(strings.ToLower(c.text)) {
			typed[w] = true
		}
	}
	switch {
	case len(f.Highlights) > 0:
		for i, h := range f.Highlights {
			if i > 0 {
				out = append(out, dimStyle.Render("…"))
			}
			for _, l := range strings.Split(strings.ReplaceAll(h, "\r", ""), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					add(emphasise(l))
				}
			}
		}
		out = append(out, "")
	case len(f.Values) == 0:
		add(dimStyle.Render(reasonWords(f, s.obj.Type)))
	default:
		for _, v := range f.Values {
			if copied {
				out = append(out, synElement.Render(v.Source))
			}
			add(markMatches(v, typed))
		}
		if f.Truncated {
			add(warnStyle.Render("Only the first 25 values of a source were analysed."))
		}
		if f.Reason == "analysis_failed" {
			add(warnStyle.Render(reasonWords(f, s.obj.Type)))
		}
		out = append(out, "")
		var toks []string
		seen := map[string]bool{}
		for _, v := range f.Values {
			for _, t := range v.Tokens {
				if seen[t.Text] {
					continue
				}
				seen[t.Text] = true
				if t.Match {
					toks = append(toks, hitStyle.Render(t.Text))
				} else {
					toks = append(toks, t.Text)
				}
			}
		}
		add(dimStyle.Render("Index tokens  ") + strings.Join(toks, " "))
	}
	if q := f.QueryTokens; q != nil {
		var parts []string
		if len(q.Typed) == len(q.Analysed) {
			for i, t := range q.Typed {
				if a := q.Analysed[i]; a != t {
					parts = append(parts, t+dimStyle.Render(" → ")+synString.Render(a))
				} else {
					parts = append(parts, t)
				}
			}
		} else {
			parts = append(parts, strings.Join(q.Typed, " ")+dimStyle.Render(" → ")+synString.Render(strings.Join(q.Analysed, " ")))
		}
		add(dimStyle.Render("Query tokens  ") + strings.Join(parts, dimStyle.Render(" · ")))
	}

	out = append(out, "")
	pad := 0
	all := append(slices.Clone(r.clauses), r.bonuses...)
	for _, c := range all {
		pad = max(pad, lipgloss.Width(clauseName(c)))
	}
	pad = min(pad+2, 28)
	for _, c := range all {
		name := clauseName(c)
		gap := strings.Repeat(" ", max(pad-lipgloss.Width(name), 1))
		p, ok := f.scores[c.text]
		if !ok {
			if !c.bonus {
				add(dimStyle.Render(name + gap + "—  no match here"))
			}
			continue
		}
		best, _ := c.best()
		switch {
		case best.field == f.Field:
			add(lipgloss.NewStyle().Bold(true).Render(name) + gap + fmt.Sprintf("%.2f  ", p) + okStyle.Render("counts in full: its best field"))
		case p == best.points:
			// Tied for best: one of the two counts in full, and it makes no
			// difference which.
			add(lipgloss.NewStyle().Bold(true).Render(name) + gap + fmt.Sprintf("%.2f  ", p) +
				okStyle.Render("counts in full: ties with "+best.field))
		default:
			add(lipgloss.NewStyle().Bold(true).Render(name) + gap + fmt.Sprintf("%.2f  ", p) +
				dimStyle.Render(fmt.Sprintf("counts 1%% (%.2f): %s scored %.2f", p*0.01, best.field, best.points)))
		}
	}
	out = append(out, "")
	out = append(out, hitStyle.Render("word")+dimStyle.Render(" matched as typed   ")+stemStyle.Render("word")+dimStyle.Render(" matched in another form"))
	return out
}

// markMatches marks a value's matched tokens: as typed, or in another form
// ("Survey" for "surveys").
func markMatches(v atlas.FieldValue, typed map[string]bool) string {
	var b strings.Builder
	at := 0
	for _, t := range v.Tokens {
		// title_split_tesim repeats a split-off word as a zero-width token.
		if !t.Match || t.Start >= t.End {
			continue
		}
		from, to := byteRange(v.Text, t.Start, t.End)
		if from < at {
			continue // overlapping tokens: the first one marked wins
		}
		b.WriteString(v.Text[at:from])
		word := v.Text[from:to]
		if typed[strings.ToLower(word)] {
			b.WriteString(hitStyle.Render(word))
		} else {
			b.WriteString(stemStyle.Render(word))
		}
		at = to
	}
	b.WriteString(v.Text[at:])
	return b.String()
}

var emRe = regexp.MustCompile(`<em>(.*?)</em>`)

// emphasise marks the words Solr's highlighter put in <em>.
func emphasise(passage string) string {
	return emRe.ReplaceAllStringFunc(passage, func(m string) string {
		return hitStyle.Render(emRe.FindStringSubmatch(m)[1])
	})
}

// explainLines is Solr's explanation tree, readably: one line per node, its
// points and what it is, with the scoring arithmetic under each field weight
// left out, and runs of fields that didn't match folded into one line. The
// parsed query follows it.
func (s searchDebugScreen) explainLines(width int) []string {
	r := s.res
	var out []string
	var walk func(n atlas.ExplainNode, depth int)
	walk = func(n atlas.ExplainNode, depth int) {
		indent := strings.Repeat("  ", depth)
		value := fmt.Sprintf("%6.2f", n.Value)
		style := lipgloss.NewStyle()
		if !n.Match {
			style = dimStyle
		}
		desc := treeWords(n, depth)
		out = append(out, ansi.Truncate(indent+style.Render(value)+"  "+desc, width, "…"))
		if weightRe.MatchString(n.Description) {
			return
		}
		misses := 0
		for _, d := range n.Details {
			if d.Description == "no matching term" {
				misses++
				continue
			}
			walk(d, depth+1)
		}
		if misses > 0 {
			out = append(out, dimStyle.Render(indent+"    0.00  no matching term in "+plural(misses, "field")))
		}
	}
	walk(r.ex.Explanation, 0)
	out = append(out, "", dimStyle.Render("Parsed query"))
	return append(out, strings.Split(wrapped(synString, r.ex.ParsedQuery, width), "\n")...)
}

// treeWords shortens a node's description: a field weight to its field and
// term, the top node's restatement of the whole query to "total".
func treeWords(n atlas.ExplainNode, depth int) string {
	if m := weightRe.FindStringSubmatch(n.Description); m != nil {
		return synElement.Render(m[1]) + dimStyle.Render(":") + m[2]
	}
	if depth == 0 && strings.HasPrefix(n.Description, "weight(FunctionScoreQuery(") {
		return "total, result of:"
	}
	return n.Description
}
