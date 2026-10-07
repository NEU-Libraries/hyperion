package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NEU-Libraries/hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestSearchRowShowsCreatorYearAndWarnings(t *testing.T) {
	year := "1851"
	row := searchRow(atlas.SearchHit{Digest: atlas.Digest{NOID: "w1", Klass: "Work", Title: "Moby-Dick"},
		Creators: []string{"Starbuck, Obed", "Melville, Herman"}, Year: &year, InProgress: true, Embargoed: true})
	if row.pid != "w1" || row.title != "Moby-Dick" || row.kind != "Work" {
		t.Errorf("row = %+v", row)
	}
	if row.note != "Starbuck, Obed · 1851" {
		t.Errorf("note = %q", row.note)
	}
	if row.warn != "in progress, embargoed" {
		t.Errorf("warn = %q", row.warn)
	}
	if bare := searchRow(atlas.SearchHit{Digest: atlas.Digest{NOID: "p1", Klass: "Person"}}); bare.note != "" || bare.warn != "" {
		t.Errorf("a hit with no creator, year or flags should have no note or warning: %+v", bare)
	}
}

func TestMatchCountSaysWhenThereAreMore(t *testing.T) {
	for _, c := range []struct {
		shown, total int
		want         string
	}{
		{1, 1, "1 match"},
		{7, 7, "7 matches"},
		{100, 312, "100 of 312 matches — add words to narrow them"},
	} {
		if got := matchCount(c.shown, c.total); got != c.want {
			t.Errorf("matchCount(%d, %d) = %q, want %q", c.shown, c.total, got, c.want)
		}
	}
}

// searchApp is signed in to a fake Atlas whose search answers body, and has
// the search prompt open. asked collects the query strings it was sent.
func searchApp(t *testing.T, body string, asked *[]string) app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.RawQuery)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	a := testApp()
	a.sess.width, a.sess.height = 100, 40
	a.sess.client = atlas.New(srv.URL, "tok")
	return navigate(t, a, enter) // Search is first on the menu
}

// loadList runs a list screen's load the way its Init would.
func loadList(t *testing.T, a app) app {
	t.Helper()
	l := a.top().(listScreen)
	res, err := l.load(context.Background(), a.sess.client)
	a, _ = sendApp(a, listMsg{id: l.id, listResult: res, err: err})
	return a
}

func TestSearchFromTheMenuOpensAHit(t *testing.T) {
	var asked []string
	a := searchApp(t, `{"results":[{"noid":"w1","klass":"Work","title":"Harbor","creators":["Hopper, Grace"],"year":"2026"},
		{"noid":"k1","klass":"Collection","title":"Maps","creators":[],"year":null}],
		"pagination":{"total":312,"page":1,"per_page":100,"pages":4}}`, &asked)
	if _, ok := a.top().(searchPromptScreen); !ok {
		t.Fatalf("Search should open the search prompt, not %T", a.top())
	}
	if v := ansi.Strip(a.View()); !strings.Contains(v, "Search the catalog") || !strings.Contains(v, "Show: All · Works") {
		t.Errorf("prompt view:\n%s", v)
	}

	a, _ = sendApp(a, typed("harbor h"))
	a, _ = sendApp(a, tea.KeyMsg{Type: tea.KeyTab}) // narrow to Works
	a = navigate(t, a, enter)
	a = loadList(t, a)
	if len(asked) != 1 || asked[0] != "page=1&per_page=100&q=harbor+h&type=Work" {
		t.Errorf("sent %v", asked)
	}

	v := ansi.Strip(a.View())
	for _, want := range []string{`Results  "harbor h" in Works · 2 of 312 matches — add words to narrow them`,
		"Harbor  Work · w1 · Hopper, Grace · 2026", "Maps  Collection · k1", "Search › Results"} {
		if !strings.Contains(v, want) {
			t.Errorf("results missing %q:\n%s", want, v)
		}
	}

	opened := navigate(t, a, enter)
	if obj, ok := opened.top().(objectScreen); !ok || obj.pid != "w1" {
		t.Fatalf("enter on a hit should open its object, not %T", opened.top())
	}

	back := navigate(t, a, esc)
	p, ok := back.top().(searchPromptScreen)
	if !ok || p.input.Value() != "harbor h" || p.searchType() != atlas.TypeWork {
		t.Errorf("esc from the results should come back to the prompt with the words and type kept")
	}
}

func TestSearchWithNoMatchesSaysSo(t *testing.T) {
	var asked []string
	a := searchApp(t, `{"results":[],"pagination":{"total":0,"page":1,"per_page":100,"pages":0}}`, &asked)
	a, _ = sendApp(a, typed("zzz"))
	a = loadList(t, navigate(t, a, enter))
	if v := ansi.Strip(a.View()); !strings.Contains(v, `Nothing matches "zzz".`) {
		t.Errorf("an empty search should say so:\n%s", v)
	}
	if !strings.Contains(asked[0], "q=zzz") || strings.Contains(asked[0], "type=") {
		t.Errorf("an unnarrowed search should send no type: %v", asked)
	}
}

func TestTheSearchTypeCyclesBothWays(t *testing.T) {
	s := newSearchPromptScreen(testSession())
	var next screen = s
	for _, want := range []atlas.Type{atlas.TypeWork, atlas.TypeCollection, atlas.TypeCommunity, atlas.TypePerson, ""} {
		next, _ = next.Update(tea.KeyMsg{Type: tea.KeyTab})
		if got := next.(searchPromptScreen).searchType(); got != want {
			t.Errorf("tab: type = %q, want %q", got, want)
		}
	}
	next, _ = next.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := next.(searchPromptScreen).searchType(); got != atlas.TypePerson {
		t.Errorf("shift+tab from All should go to People, got %q", got)
	}
}

func TestSearchPromptTakesQAndH(t *testing.T) {
	var asked []string
	a := searchApp(t, `{}`, &asked)
	a, _ = sendApp(a, typed("q"))
	a, _ = sendApp(a, typed("h"))
	if a.quitting || len(a.stack) != 2 || a.top().(searchPromptScreen).input.Value() != "qh" {
		t.Error("q and h in the search prompt should be letters")
	}
	if _, cmd := sendApp(newSearchApp(a), enter); cmd != nil {
		t.Error("enter with no words should do nothing")
	}
}

// newSearchApp is a with its search prompt emptied.
func newSearchApp(a app) app {
	a.stack = append(a.below(), newSearchPromptScreen(a.sess))
	return a
}
