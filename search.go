package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/NEU-Libraries/hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// searchPromptScreen asks for the words to search the catalog for, and
// which type to narrow to. It stays under the results, so esc from them comes
// back here with the words kept.
type searchPromptScreen struct {
	sess    *session
	input   textinput.Model
	typeIdx int // 0 = every type, else atlas.SearchTypes[typeIdx-1]
}

func newSearchPromptScreen(sess *session) searchPromptScreen {
	in := textinput.New()
	in.Prompt = "Search: "
	in.Placeholder = "words from a title, creator, or description"
	in.CharLimit = 200
	in.Focus()
	return searchPromptScreen{sess: sess, input: in}
}

func (s searchPromptScreen) Init() tea.Cmd { return textinput.Blink }

func (s searchPromptScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyEnter:
			v := strings.TrimSpace(s.input.Value())
			if v == "" {
				return s, nil
			}
			s.input.SetValue(v)
			return s, push(newSearchResults(s.sess, v, s.searchType()))
		case tea.KeyTab:
			s.typeIdx = (s.typeIdx + 1) % (len(atlas.SearchTypes) + 1)
			return s, nil
		case tea.KeyShiftTab:
			s.typeIdx = (s.typeIdx + len(atlas.SearchTypes)) % (len(atlas.SearchTypes) + 1)
			return s, nil
		case tea.KeyEsc:
			return s, pop
		}
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

// searchType is the type the search is narrowed to, "" for every type.
func (s searchPromptScreen) searchType() atlas.Type {
	if s.typeIdx == 0 {
		return ""
	}
	return atlas.SearchTypes[s.typeIdx-1]
}

func (s searchPromptScreen) crumb() string { return "Search" }

func (s searchPromptScreen) typing() bool { return true }

func (s searchPromptScreen) View() string {
	names := []string{"All"}
	for _, t := range atlas.SearchTypes {
		names = append(names, typePlural(t))
	}
	for i, n := range names {
		if i == s.typeIdx {
			names[i] = selStyle.Render(n)
		} else {
			names[i] = dimStyle.Render(n)
		}
	}
	body := s.input.View() + "\n\n" + dimStyle.Render("Show: ") + strings.Join(names, dimStyle.Render(" · "))
	return panel(s.sess, titleStyle.Render("Search the catalog"), body, "enter to search · tab to narrow · esc to go back")
}

// typePlural names a search type's objects, for the type filter.
func typePlural(t atlas.Type) string {
	if t == atlas.TypeCommunity {
		return "Communities"
	}
	if t == atlas.TypePerson {
		return "People"
	}
	return string(t) + "s"
}

// newSearchResults lists the first page of hits for q. A search can match
// thousands of objects, so it reads one page of atlas.SearchPageSize and says
// when there are more, rather than walking every page.
func newSearchResults(sess *session, q string, t atlas.Type) listScreen {
	about := fmt.Sprintf("%q", q)
	if t != "" {
		about += " in " + typePlural(t)
	}
	return newResultListScreen(sess, "Results", about, fmt.Sprintf("Nothing matches %s.", about),
		func(ctx context.Context, c *atlas.Client) (listResult, error) {
			res, err := c.Search(ctx, q, t, 1)
			if err != nil {
				return listResult{}, err
			}
			items := make([]listItem, len(res.Results))
			for i, h := range res.Results {
				items[i] = searchRow(h)
			}
			return listResult{items: items, about: about + " · " + matchCount(len(items), res.Pagination.Total)}, nil
		}).withRaw(atlas.PathSearch(q, t, 1, atlas.SearchPageSize), "search")
}

// matchCount says how many hits there are, and how to see the rest when the
// page doesn't hold them all.
func matchCount(shown, total int) string {
	switch {
	case total == 1:
		return "1 match"
	case total <= shown:
		return fmt.Sprintf("%d matches", total)
	}
	return fmt.Sprintf("%d of %d matches — add words to narrow them", shown, total)
}

// searchRow is a hit as a list row: its first creator and year as the note
// ("Starbuck, Obed · 1851"), and any of its unfinished states as the warning.
func searchRow(h atlas.SearchHit) listItem {
	var note []string
	if len(h.Creators) > 0 {
		note = append(note, h.Creators[0])
	}
	if h.Year != nil && *h.Year != "" {
		note = append(note, *h.Year)
	}
	var warn []string
	for _, f := range []struct {
		on   bool
		word string
	}{{h.InProgress, "in progress"}, {h.Embargoed, "embargoed"}, {h.Incomplete, "incomplete"}} {
		if f.on {
			warn = append(warn, f.word)
		}
	}
	return listItem{pid: h.NOID, title: h.Title, kind: h.Klass,
		note: strings.Join(note, " · "), warn: strings.Join(warn, ", ")}
}
