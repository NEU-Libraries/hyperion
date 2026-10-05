package main

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"hyperion/internal/atlas"
)

// A reading of Solr's explanation of one object's score, after Cerberus's
// SearchExplanation. Edismax scores each search word (or quoted phrase) with
// one "max plus 0.01 times others" node, holding a weight per field the word
// matched in: the best field counts in full and the others add 1% each. The
// phrase boost (pf) is one more such node, whose weights name a phrase. A
// boost function multiplies the whole. Anything else in the tree is left
// out here; the explanation view shows the tree itself.

// weightRe reads a field weight's description: "weight(title_tsim:coastal in
// 144) [SchemaSimilarity], result of:", or a phrase, `title_tsim:"coastal
// surveys"~2`.
var weightRe = regexp.MustCompile(`^weight\(([A-Za-z0-9_]+):("[^"]*"(?:~\d+)?|\S+) in \d+\)`)

// requiredRe finds the minimum-match count edismax wrote into the parsed
// query: the "~2" closing the word clauses. A phrase's slop follows a quote,
// and the tie follows a decimal point, so neither matches.
var requiredRe = regexp.MustCompile(`\)~(\d+)\)`)

// minMatchRe reads the count from a failed minimum match.
var minMatchRe = regexp.MustCompile(`Failure to match minimum number of optional clauses: (\d+)`)

// fieldScore is one field's weight for a word or phrase.
type fieldScore struct {
	field  string
	term   string // as Solr searched it: "survey", or "coastal surveys" for a phrase
	points float64
}

// clause is one thing the words were scored on: a word, a quoted phrase, or
// the phrase boost. hits is every field it matched in, strongest first; none
// when it matched nowhere.
type clause struct {
	text   string // the word as typed, or the phrase
	phrase bool
	bonus  bool // the phrase boost, which adds to a match but can't make one
	points float64
	hits   []fieldScore
}

func (c clause) best() (fieldScore, bool) {
	if len(c.hits) == 0 {
		return fieldScore{}, false
	}
	return c.hits[0], true
}

// fieldReading is one qf field: what Atlas sent for it, its boost, and what
// each clause scored in it.
type fieldReading struct {
	atlas.ExplainedField
	boost  float64
	scores map[string]float64 // clause text → points in this field
}

// points is the field's strongest single score.
func (f fieldReading) points() float64 {
	best := 0.0
	for _, p := range f.scores {
		best = max(best, p)
	}
	return best
}

// hasText reports whether the field has stored text to show.
func (f fieldReading) hasText() bool { return len(f.Values) > 0 || len(f.Highlights) > 0 }

// reading is a whole explanation, read.
type reading struct {
	ex       atlas.SearchExplanation
	clauses  []clause // the words and quoted phrases, in the order typed
	bonuses  []clause // the phrase boost, if it scored
	fields   []fieldReading
	required int     // how many clauses must match
	found    int     // how many did
	adjust   float64 // the boost function's multiplier, 1 when none
}

// readExplanation interprets what Atlas sent.
func readExplanation(ex atlas.SearchExplanation) reading {
	r := reading{ex: ex, adjust: 1}
	typed, quoted := typedClauses(ex)

	nodes := collect(ex.Explanation, func(n atlas.ExplainNode) bool {
		return strings.HasPrefix(n.Description, "max plus")
	})
	scored := map[string]clause{}
	var order []string
	for _, n := range nodes {
		c := clauseAt(n)
		if len(c.hits) == 0 {
			continue
		}
		if c.phrase {
			c.text = c.hits[0].term
			_, again := scored[matchQuoted(c.text, quoted)]
			c.bonus = again || isBonus(c, ex, quoted) // a phrase scored twice: the boost is the second
			if c.bonus {
				r.bonuses = append(r.bonuses, c)
				continue
			}
			c.text = matchQuoted(c.text, quoted)
		} else {
			c.text = typedForm(c.hits, ex.Fields, typed)
		}
		if _, seen := scored[c.text]; !seen {
			order = append(order, c.text)
		}
		scored[c.text] = c
	}

	// The clauses in the order typed: quoted phrases first, as edismax
	// lists them, then the words. A clause the tree doesn't name matched
	// nowhere; one only the tree names (a stop word's oddity) goes last.
	for _, text := range append(append([]string{}, quoted...), typed...) {
		c, ok := scored[text]
		if !ok {
			c = clause{text: text, phrase: slices.Contains(quoted, text)}
		} else {
			r.found++
		}
		delete(scored, text)
		r.clauses = append(r.clauses, c)
	}
	for _, text := range order {
		if c, ok := scored[text]; ok {
			r.found++
			r.clauses = append(r.clauses, c)
		}
	}

	r.required = requiredClauses(ex, len(r.clauses))
	for _, n := range collect(ex.Explanation, func(n atlas.ExplainNode) bool {
		return strings.HasPrefix(n.Description, "if(")
	}) {
		r.adjust *= n.Value
	}
	for _, f := range ex.Fields {
		r.fields = append(r.fields, readField(f, ex.Handler.QF[f.Field], append(slices.Clone(r.clauses), r.bonuses...)))
	}
	return r
}

// clauseAt reads one "max plus" node: every field its weights scored in,
// strongest first. A quoted term marks the clause a phrase.
func clauseAt(n atlas.ExplainNode) clause {
	c := clause{points: n.Value}
	for _, w := range collect(n, func(n atlas.ExplainNode) bool { return weightRe.MatchString(n.Description) }) {
		m := weightRe.FindStringSubmatch(w.Description)
		term := m[2]
		if strings.HasPrefix(term, `"`) {
			c.phrase = true
			term = strings.Trim(strings.Split(term, `"~`)[0], `"`)
		}
		c.hits = append(c.hits, fieldScore{field: m[1], term: term, points: w.Value})
	}
	slices.SortStableFunc(c.hits, func(a, b fieldScore) int { return cmp.Compare(b.points, a.points) })
	return c
}

// requiredClauses is how many clauses must match: mm from the parsed query,
// else from a failed minimum-match node, else all n of them.
func requiredClauses(ex atlas.SearchExplanation, n int) int {
	if m := requiredRe.FindStringSubmatch(ex.ParsedQuery); m != nil {
		n, _ = strconv.Atoi(m[1])
	} else if failed := collect(ex.Explanation, func(n atlas.ExplainNode) bool {
		return minMatchRe.MatchString(n.Description)
	}); len(failed) > 0 {
		n, _ = strconv.Atoi(minMatchRe.FindStringSubmatch(failed[0].Description)[1])
	}
	return n
}

// readField is what each clause scored in one field, at its best.
func readField(f atlas.ExplainedField, boost float64, clauses []clause) fieldReading {
	fr := fieldReading{ExplainedField: f, boost: boost, scores: map[string]float64{}}
	for _, c := range clauses {
		for _, h := range c.hits {
			if h.field == f.Field {
				fr.scores[c.text] = max(fr.scores[c.text], h.points)
			}
		}
	}
	return fr
}

// collect gathers the nodes under n (n included) that want accepts.
func collect(n atlas.ExplainNode, want func(atlas.ExplainNode) bool) []atlas.ExplainNode {
	var found []atlas.ExplainNode
	if want(n) {
		found = append(found, n)
	}
	for _, d := range n.Details {
		found = append(found, collect(d, want)...)
	}
	return found
}

// quotedRe finds the phrases the words put in quotes.
var quotedRe = regexp.MustCompile(`"([^"]+)"`)

// typedClauses lists the words as the search split them, and the quoted
// phrases, each once. The words come from the first field's query side, so
// they are as typed but split as Solr splits them ("shoreline-change" is
// two words); a word inside quotes belongs to its phrase.
func typedClauses(ex atlas.SearchExplanation) (words, quoted []string) {
	inQuotes := map[string]bool{}
	for _, m := range quotedRe.FindAllStringSubmatch(ex.Q, -1) {
		phrase := strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
		if phrase != "" && !slices.Contains(quoted, phrase) {
			quoted = append(quoted, phrase)
		}
		for _, w := range strings.FieldsFunc(phrase, notWordRune) {
			inQuotes[w] = true
		}
	}
	for _, f := range ex.Fields {
		if f.QueryTokens == nil {
			continue
		}
		seen := map[string]bool{}
		for _, w := range f.QueryTokens.Typed {
			low := strings.ToLower(w)
			if !inQuotes[low] && !seen[low] {
				seen[low] = true
				words = append(words, w)
			}
		}
		break
	}
	return words, quoted
}

func notWordRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '’'
}

// typedForm names the word a node scored, as typed. Solr's tree holds only
// the searched form ("survey"), so it is matched against each field's query
// side, which pairs the typed word with its searched form.
func typedForm(hits []fieldScore, fields []atlas.ExplainedField, typed []string) string {
	for _, h := range hits {
		if w, ok := typedAs(h.term, typed); ok {
			return w
		}
	}
	for _, h := range hits {
		for _, f := range fields {
			q := f.QueryTokens
			if f.Field != h.field || q == nil || len(q.Typed) != len(q.Analysed) {
				continue
			}
			for i, a := range q.Analysed {
				if w, ok := typedAs(q.Typed[i], typed); ok && a == h.term {
					return w
				}
			}
		}
	}
	return hits[0].term
}

// typedAs is word as the words typed it, ignoring case.
func typedAs(word string, typed []string) (string, bool) {
	for _, w := range typed {
		if strings.EqualFold(w, word) {
			return w, true
		}
	}
	return "", false
}

// isBonus tells the phrase boost from a quoted phrase. With nothing quoted,
// any phrase is the boost. Otherwise the boost is the phrase scored only in
// pf's fields whose length is no quoted phrase's: it searches every word.
func isBonus(c clause, ex atlas.SearchExplanation, quoted []string) bool {
	if len(quoted) == 0 {
		return true
	}
	for _, h := range c.hits {
		if _, ok := ex.Handler.PF[h.field]; !ok {
			return false
		}
	}
	n := len(strings.Fields(c.text))
	for _, q := range quoted {
		if len(strings.Fields(q)) == n {
			return false
		}
	}
	return true
}

// matchQuoted names a quoted phrase as the words put it, when the tree's
// form (perhaps stemmed) is one of them; else as the tree has it.
func matchQuoted(term string, quoted []string) string {
	for _, q := range quoted {
		if strings.EqualFold(q, term) {
			return q
		}
	}
	for _, q := range quoted {
		if len(strings.Fields(q)) == len(strings.Fields(term)) {
			return q
		}
	}
	return term
}

// hiddenWords says what each catalog filter in hidden_by means.
var hiddenWords = map[string]string{
	"types":            "its type isn't searched",
	"tombstoned":       "it is tombstoned",
	"featured":         "it is a featured Collection",
	"personal_root":    "it is a personal root Collection",
	"system_container": "it is a system container",
}

// byteRange converts a token's UTF-16 offsets, as Solr counts, into byte
// offsets into text, as Go slices it. Out-of-range offsets are clamped.
func byteRange(text string, start, end int) (int, int) {
	units, from, to := 0, len(text), len(text)
	for i, r := range text {
		if units >= start && from == len(text) {
			from = i
		}
		if units >= end {
			to = i
			break
		}
		units += len(utf16.Encode([]rune{r}))
	}
	return min(from, to), to
}

// nearestWord is an indexed word that looks like w but did not match it —
// "survey" for "surveying" — to suggest why w matched nowhere. It shares a
// prefix of at least four letters; "" when no indexed word does.
func nearestWord(w string, fields []fieldReading) string {
	w = strings.ToLower(w)
	best, bestLen := "", 3
	for _, f := range fields {
		for _, v := range f.Values {
			for _, t := range v.Tokens {
				n := 0
				for n < len(t.Text) && n < len(w) && t.Text[n] == w[n] {
					n++
				}
				if n > bestLen && t.Text != w {
					best, bestLen = t.Text, n
				}
			}
		}
	}
	return best
}
