package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"hyperion/internal/atlas"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// indexLimit is why the Solr document and Search debug won't open for anyone
// but an admin or a delegated admin.
const indexLimit = "The Solr document and Search debug are only available to admins and delegated admins."

// notIndexed words an object that Atlas has but Solr doesn't: a fault in the
// index, not in the PID.
func notIndexed(t atlas.Type) userErr {
	return userErr("Solr holds no document for this " + string(t) + ", so it can't appear in search. " +
		"Reindexing it in Atlas fixes this.")
}

func modsJSONSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "MODS JSON", objectContext(obj), func(ctx context.Context, c *atlas.Client) (doc, error) {
		body, err := c.Raw(ctx, atlas.PathMODSJSON(obj.NOID))
		if errors.Is(err, atlas.ErrNotFound) {
			return doc{}, notice("This " + string(obj.Type) + " has no MODS.")
		}
		return doc{render: jsonRender(indentJSON(body)), raw: body}, err
	}).withRaw(atlas.PathMODSJSON(obj.NOID), stem(obj, "mods"))
}

func solrSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "Solr document", objectContext(obj), func(ctx context.Context, c *atlas.Client) (doc, error) {
		fields, err := c.SolrDocument(ctx, obj.NOID)
		switch {
		case errors.Is(err, atlas.ErrForbidden):
			return doc{}, notice(indexLimit)
		case errors.Is(err, atlas.ErrNotIndexed):
			return doc{}, notIndexed(obj.Type)
		case err != nil:
			return doc{}, err
		}
		return doc{render: solrDocRender(fields, slices.Contains(atlas.ExplainedTypes, obj.Type))}, nil
	}).withRaw(atlas.PathSolr(obj.NOID), stem(obj, "solr"))
}

// solrField is one row of the Solr document: a field, or the variants of one
// field that hold the same values ("depositor_ssi", "depositor_tesim", …),
// which Atlas writes for most properties.
type solrField struct {
	name   string
	values []string
}

// groupSolrFields folds a field's variants into one row when their values
// match, named "depositor_{ssi,ssim,tesi}". The names stay exact, so the row
// still says which fields exist. Rows keep Solr's order of their first field.
func groupSolrFields(fields []atlas.Field) []solrField {
	type group struct {
		base     string
		suffixes []string
		values   []string
	}
	var groups []*group
	for _, f := range fields {
		values := solrValues(f.Value)
		base, suffix := splitSuffix(f.Key)
		var into *group
		if suffix != "" {
			for _, g := range groups {
				if g.base == base && len(g.suffixes) > 0 && slices.Equal(g.values, values) {
					into = g
					break
				}
			}
		}
		if into == nil {
			into = &group{base: base, values: values}
			groups = append(groups, into)
		}
		if suffix != "" {
			into.suffixes = append(into.suffixes, suffix)
		}
	}
	rows := make([]solrField, len(groups))
	for i, g := range groups {
		name := g.base
		switch len(g.suffixes) {
		case 0:
		case 1:
			name += "_" + g.suffixes[0]
		default:
			slices.Sort(g.suffixes)
			name += "_{" + strings.Join(g.suffixes, ",") + "}"
		}
		rows[i] = solrField{name: name, values: g.values}
	}
	return rows
}

// splitSuffix splits a dynamic field's name at its type suffix: "title_tsim"
// is "title" and "tsim". A name with no lower-case suffix ("id",
// "_version_") has none.
func splitSuffix(key string) (string, string) {
	i := strings.LastIndexByte(key, '_')
	if i <= 0 || i == len(key)-1 {
		return key, ""
	}
	for _, r := range key[i+1:] {
		if !unicode.IsLower(r) {
			return key, ""
		}
	}
	return key[:i], key[i+1:]
}

// solrValues is a field's value or values, each as one string.
func solrValues(raw json.RawMessage) []string {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		list = []json.RawMessage{raw}
	}
	out := make([]string, len(list))
	for i, v := range list {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[i] = s
		} else {
			out[i] = string(v)
		}
	}
	return out
}

// solrValueLimit is how much of a long value (the MODS XML, the full text)
// the document shows; the raw view has it whole.
const solrValueLimit = 160

// solrDocRender lays the document out as a field/value table, one value per
// line (see valueLines), each cut to a line. A note heads it, so that a searched field missing
// from the document isn't taken for a fault.
func solrDocRender(fields []atlas.Field, explained bool) renderFunc {
	rows := groupSolrFields(fields)
	return func(width int) string {
		about := "Solr shows only stored fields. Fields that are searched but not stored, such as the stemmed " +
			"titles and the keywords field, never appear here."
		if explained {
			about += " Search debug shows what they match."
		}
		var b strings.Builder
		b.WriteString(wrapped(dimStyle, about, width) + "\n\n")

		nameWidth := 0
		for _, r := range rows {
			nameWidth = max(nameWidth, lipgloss.Width(r.name))
		}
		if width > 0 { // up to half the view; a longer name puts its values under it
			nameWidth = min(nameWidth, max(width/2, 24))
		}
		indent := strings.Repeat(" ", nameWidth+2)
		for _, r := range rows {
			lines := valueLines(r.values)
			if len(lines) == 0 {
				lines = []string{dimStyle.Render("none")}
			}
			name := synElement.Render(r.name)
			if lipgloss.Width(r.name) > nameWidth { // too long for the column: values go under it
				b.WriteString(name + "\n")
				name = strings.Repeat(" ", nameWidth)
			} else {
				name += strings.Repeat(" ", nameWidth-lipgloss.Width(r.name))
			}
			b.WriteString(name + "  " + lines[0] + "\n")
			for _, l := range lines[1:] {
				b.WriteString(indent + l + "\n")
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}
}

// solrValueRows is how many lines one field's values may take; the raw
// view has them all.
const solrValueRows = 8

// valueLines shows a field's values a line each, a run of one value as one
// line with its count ("Work ×3"), and no more than solrValueRows lines.
func valueLines(values []string) []string {
	var lines []string
	for i := 0; i < len(values); {
		j := i + 1
		for j < len(values) && values[j] == values[i] {
			j++
		}
		line := solrValueLine(values[i])
		if j-i > 1 {
			line += dimStyle.Render(fmt.Sprintf("  ×%d", j-i))
		}
		lines = append(lines, line)
		i = j
	}
	if len(lines) > solrValueRows {
		more := len(lines) - (solrValueRows - 1)
		lines = append(lines[:solrValueRows-1], dimStyle.Render(fmt.Sprintf("… %d more; r shows them all", more)))
	}
	return lines
}

// solrValueLine shows one value on one line: line breaks as ⏎, and a long
// value cut, saying how long it is.
func solrValueLine(v string) string {
	if strings.TrimSpace(v) == "" {
		return dimStyle.Render("(blank)")
	}
	one := strings.NewReplacer("\r\n", "⏎", "\n", "⏎", "\r", "⏎", "\t", " ").Replace(v)
	if n := len([]rune(v)); n > solrValueLimit {
		return ansi.Truncate(one, solrValueLimit, "") + dimStyle.Render(fmt.Sprintf(" … %s characters; r shows it whole", thousands(n)))
	}
	return one
}

// thousands writes n with comma separators: 12,345.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
