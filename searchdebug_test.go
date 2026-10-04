package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The fixtures are Atlas 0.6.218's answers for Works in the development
// repository, captured on 3 October 2026.
func fixture(t *testing.T, name string) atlas.SearchExplanation {
	t.Helper()
	body, err := os.ReadFile("testdata/search_explanation/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var ex atlas.SearchExplanation
	if err := json.Unmarshal(body, &ex); err != nil {
		t.Fatal(err)
	}
	return ex
}

var coastal = atlas.Object{Type: atlas.TypeWork, NOID: "zcrjh61", Title: "Coastal Resilience Survey Dataset, 2024-2026"}

// debugScreen is a Search debug screen showing a fixture's answer.
func debugScreen(t *testing.T, name string, width, height int) searchDebugScreen {
	t.Helper()
	sess := testSession()
	sess.width, sess.height = width, height
	s := searchDebugSection(sess, coastal).(searchDebugScreen)
	ex := fixture(t, name)
	s.input.SetValue(ex.Q)
	s.asked = ex.Q
	s.take(ex, nil)
	return s
}

func clauseTexts(cs []clause) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.text)
	}
	return out
}

func fieldNamed(r reading, name string) fieldReading {
	for _, f := range r.fields {
		if f.Field == name {
			return f
		}
	}
	return fieldReading{}
}

func TestReadAMatch(t *testing.T) {
	r := readExplanation(fixture(t, "zcrjh61_coastal_surveys"))
	if got := clauseTexts(r.clauses); !slices.Equal(got, []string{"coastal", "surveys"}) {
		t.Fatalf("clauses = %v", got)
	}
	if r.required != 2 || r.found != 2 || len(r.bonuses) != 0 || r.adjust != 1 {
		t.Errorf("required %d, found %d, bonuses %d, adjust %v", r.required, r.found, len(r.bonuses), r.adjust)
	}
	best, _ := r.clauses[0].best()
	if best.field != "title_tsim" || len(r.clauses[0].hits) != 7 {
		t.Errorf("coastal: best %+v of %d hits", best, len(r.clauses[0].hits))
	}
	// Solr searched "survey"; the word as typed is "surveys", and the plain
	// title, which doesn't stem, didn't match it.
	if best, _ := r.clauses[1].best(); best.term != "survey" || best.points < 5 {
		t.Errorf("surveys: best %+v", best)
	}
	if title := fieldNamed(r, "title_tsim"); title.scores["coastal"] == 0 || title.scores["surveys"] != 0 || title.boost != 10 {
		t.Errorf("title_tsim = %+v", title.scores)
	}
	if stem := fieldNamed(r, "title_stem_tesim"); stem.scores["surveys"] == 0 {
		t.Errorf("title_stem_tesim = %+v", stem.scores)
	}
}

func TestReadAMissThatFailsMinimumMatch(t *testing.T) {
	r := readExplanation(fixture(t, "zcrjh61_coastal_whaling_logbook"))
	if r.ex.Matched || r.required != 2 || r.found != 1 {
		t.Errorf("matched %v, required %d, found %d", r.ex.Matched, r.required, r.found)
	}
	if got := clauseTexts(r.clauses); !slices.Equal(got, []string{"coastal", "whaling", "logbook"}) {
		t.Fatalf("clauses = %v", got)
	}
	if len(r.clauses[1].hits) != 0 || len(r.clauses[0].hits) == 0 {
		t.Errorf("only coastal matched: %+v", r.clauses)
	}
	v := ansi.Strip(debugScreen(t, "zcrjh61_coastal_whaling_logbook", 140, 50).verdict())
	for _, want := range []string{"✗ No match · needs 2 of 3, 1 found", "whaling  in no field", "minimum match 2<-1 5<-2 6<90% → 2 of 3 required"} {
		if !strings.Contains(v, want) {
			t.Errorf("verdict missing %q:\n%s", want, v)
		}
	}
}

func TestReadAQuotedPhraseAndThePhraseBonus(t *testing.T) {
	r := readExplanation(fixture(t, "zcrjh61_quoted_phrase"))
	if got := clauseTexts(r.clauses); !slices.Equal(got, []string{"coastal resilience", "survey"}) || !r.clauses[0].phrase {
		t.Errorf("clauses = %v", got)
	}
	if len(r.bonuses) != 0 {
		t.Errorf("a quoted phrase is a clause, not the bonus: %+v", r.bonuses)
	}

	r = readExplanation(fixture(t, "7wm38gk_sample_presentation"))
	if got := clauseTexts(r.clauses); !slices.Equal(got, []string{"sample", "presentation"}) {
		t.Errorf("clauses = %v", got)
	}
	if len(r.bonuses) != 1 || r.bonuses[0].text != "sample presentation" || len(r.bonuses[0].hits) != 2 {
		t.Fatalf("bonuses = %+v", r.bonuses)
	}
	if f := fieldNamed(r, "title_tsim"); f.points() != f.scores["sample presentation"] {
		t.Errorf("the bonus is title_tsim's strongest score: %+v", f.scores)
	}
}

func TestVerdictNamesTheFilterThatHidesAMatch(t *testing.T) {
	v := ansi.Strip(debugScreen(t, "h1894jn_datasets", 140, 50).verdict())
	if !strings.Contains(v, "✓ Matches · score 15.54 · but catalog search never shows it: it is a featured Collection") {
		t.Errorf("verdict:\n%s", v)
	}
}

func TestFullTextShowsTheHighlights(t *testing.T) {
	s := debugScreen(t, "k98sgx6_lorem_ipsum", 140, 50)
	if s.sel != "full_text_tesimv" {
		t.Fatalf("the strongest field is selected first: %s", s.sel)
	}
	detail := ansi.Strip(strings.Join(s.fieldLines(s.selected(), 60), "\n"))
	if !strings.Contains(detail, "Lorem ipsum lorem") || strings.Contains(detail, "<em>") {
		t.Errorf("detail:\n%s", detail)
	}
}

func TestByteRangeCountsUTF16(t *testing.T) {
	text := "Café 𝄞 notes" // é is one UTF-16 unit and two bytes; 𝄞 is two units and four bytes
	for _, c := range []struct {
		start, end int
		want       string
	}{
		{0, 4, "Café"}, {5, 7, "𝄞"}, {8, 13, "notes"}, {8, 99, "notes"},
	} {
		from, to := byteRange(text, c.start, c.end)
		if got := text[from:to]; got != c.want {
			t.Errorf("byteRange(%d, %d) = %q, want %q", c.start, c.end, got, c.want)
		}
	}
}

func TestMatchesAreMarkedAsTypedOrInAnotherForm(t *testing.T) {
	withColour(t)
	r := readExplanation(fixture(t, "zcrjh61_coastal_surveys"))
	v := fieldNamed(r, "title_stem_tesim").Values[0]
	got := markMatches(v, map[string]bool{"coastal": true, "surveys": true})
	if !strings.Contains(got, hitStyle.Render("Coastal")) || !strings.Contains(got, stemStyle.Render("Survey")) {
		t.Errorf("marked = %q", got)
	}
	if ansi.Strip(got) != v.Text {
		t.Errorf("marking changed the text: %q", ansi.Strip(got))
	}
}

func TestSearchDebugFitsTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{140, 50}, {100, 40}, {80, 40}, {80, 30}} {
		for _, explain := range []bool{false, true} {
			s := debugScreen(t, "zcrjh61_coastal_surveys", size[0], size[1])
			s.explain = explain
			// As the app shows it: the key row ends with how to quit,
			// shortened to fit.
			lines := strings.Split(ansi.Strip(withQuitHint(s.View(), "ctrl+c to quit", size[0]-1)), "\n")
			if room := size[1] - headerLines - 1; len(lines) > room {
				t.Errorf("%dx%d (explanation %v): %d lines, room for %d", size[0], size[1], explain, len(lines), room)
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > size[0]-1 {
					t.Errorf("%dx%d: line %d is %d wide", size[0], size[1], i, w)
				}
			}
		}
	}
}

func TestSearchDebugKeys(t *testing.T) {
	s := debugScreen(t, "zcrjh61_coastal_surveys", 140, 50)
	send := func(msg tea.Msg) {
		next, _ := s.Update(msg)
		s = next.(searchDebugScreen)
	}
	send(tea.KeyMsg{Type: tea.KeyDown})
	if s.sel != "description_tsim" {
		t.Errorf("↓ moves to the next strongest field, got %s", s.sel)
	}
	send(enter)
	if !s.explain || !strings.Contains(ansi.Strip(s.View()), "max plus 0.01 times others of:") {
		t.Errorf("enter shows the explanation tree")
	}
	send(esc)
	if s.explain {
		t.Error("esc leaves the explanation first")
	}
	if _, cmd := s.Update(esc); !isPop(cmd) {
		t.Error("then esc goes back")
	}
	if !s.typing() {
		t.Error("q is a letter here, not quit")
	}
}

// TestTypingAsksAtlasOnceItPauses drives the debounce: each edit schedules a
// tick, only the latest tick asks, and only the latest answer is shown.
func TestTypingAsksAtlasOnceItPauses(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		switch r.URL.Query().Get("q") {
		case "secret":
			w.WriteHeader(http.StatusForbidden)
		case "gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_indexed"}`))
		case "down":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"solr_unavailable"}`))
		default:
			body, _ := os.ReadFile("testdata/search_explanation/zcrjh61_coastal_surveys.json")
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(srv.Close)
	sess := testSession()
	sess.width, sess.height = 140, 50
	sess.client = atlas.New(srv.URL, "tok")
	var s screen = searchDebugSection(sess, coastal)

	typeWords := func(words string) {
		d := s.(searchDebugScreen)
		d.input.SetValue("")
		s = d
		for _, r := range words {
			s, _ = s.Update(typed(string(r)))
		}
	}
	answer := func(seq int) {
		t.Helper()
		next, cmd := s.Update(debounceMsg{id: s.(searchDebugScreen).id, seq: seq})
		s = next
		if cmd == nil {
			t.Fatal("the latest pause asks Atlas")
		}
		s, _ = s.Update(initResult[explainMsg](t, cmd))
	}

	typeWords("coastal surveys")
	d := s.(searchDebugScreen)
	if next, cmd := s.Update(debounceMsg{id: d.id, seq: d.seq - 1}); cmd != nil || next.(searchDebugScreen).busy {
		t.Error("a pause between keys that typing has passed asks nothing")
	}
	answer(d.seq)
	if v := ansi.Strip(s.View()); !strings.Contains(v, "✓ Matches · score 15.85") || !strings.Contains(v, "› title_tsim") {
		t.Errorf("view:\n%s", v)
	}
	if len(asked) != 1 || asked[0] != "/resources/zcrjh61/search_explanation?q=coastal+surveys" {
		t.Errorf("asked %v", asked)
	}

	for words, want := range map[string]string{
		"secret": "only available to admins and delegated admins",
		"gone":   "Solr holds no document for this Work",
		"down":   "Atlas couldn't reach Solr",
	} {
		typeWords(words)
		answer(s.(searchDebugScreen).seq)
		if v := ansi.Strip(s.View()); !strings.Contains(v, want) || strings.Contains(v, "Fields") {
			t.Errorf("%s: view:\n%s", words, v)
		}
	}
}

func TestSearchSectionsSayWhoCanOpenThem(t *testing.T) {
	sess := testSession()
	o := newObjectScreen(sess, "zcrjh61")
	next, _ := o.Update(objectMsg{id: o.id, obj: coastal})
	labels := labelsOf(next.(objectScreen).sections)
	if !strings.Contains(labels, "Solr document (admins only), Search debug (admins only)") {
		t.Errorf("a standard user's sections: %s", labels)
	}
	sess.user.Role = "admin"
	next, _ = o.Update(objectMsg{id: o.id, obj: coastal})
	if labels := labelsOf(next.(objectScreen).sections); !strings.Contains(labels, "Solr document, Search debug, Permissions") {
		t.Errorf("an admin's sections: %s", labels)
	}
}

func TestATieCountsInFull(t *testing.T) {
	s := debugScreen(t, "zcrjh61_coastal_surveys", 140, 50)
	r := s.res
	detail := ansi.Strip(strings.Join(s.fieldLines(fieldNamed(*r, "title_stem_tesim"), 80), "\n"))
	if !strings.Contains(detail, "surveys  5.18  counts in full: ties with title_kstem_tesim") ||
		!strings.Contains(detail, "coastal  5.18  counts 1% (0.05): title_tsim scored 10.36") {
		t.Errorf("detail:\n%s", detail)
	}
}
