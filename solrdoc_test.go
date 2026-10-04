package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"hyperion/internal/atlas"

	"github.com/charmbracelet/x/ansi"
)

func solrFixture(t *testing.T) []atlas.Field {
	t.Helper()
	body, err := os.ReadFile("testdata/solr_zcrjh61.json")
	if err != nil {
		t.Fatal(err)
	}
	var wrap struct{ Document json.RawMessage }
	if err := json.Unmarshal(body, &wrap); err != nil {
		t.Fatal(err)
	}
	fields, err := atlas.OrderedFields(wrap.Document)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestSolrDocumentFoldsVariantsThatAgree(t *testing.T) {
	rows := map[string][]string{}
	for _, r := range groupSolrFields(solrFixture(t)) {
		rows[r.name] = r.values
	}
	for name, want := range map[string]string{
		"_version_": "",
		"id":        "",
		"alternate_ids_{ssi,ssim,tesi,tesim,tsi,tsim}": "id-zcrjh61",
		"title_tsim":                          "Coastal Resilience Survey Dataset, 2024-2026",
		"title_ssi":                           "coastal resilience survey dataset 20242026", // the sort key differs, so stands apart
		"internal_resource_{ssim,tesim,tsim}": "Work",
	} {
		got, ok := rows[name]
		if !ok {
			t.Errorf("no row %q", name)
			continue
		}
		if want != "" && (len(got) != 1 || got[0] != want) {
			t.Errorf("%s = %v, want %q", name, got, want)
		}
	}
}

func TestSplitSuffix(t *testing.T) {
	for key, want := range map[string][2]string{
		"title_tsim":       {"title", "tsim"},
		"a_member_of_ssim": {"a_member_of", "ssim"},
		"id":               {"id", ""},
		"_version_":        {"_version_", ""},
		"storage_bytes_ls": {"storage_bytes", "ls"},
	} {
		if base, suffix := splitSuffix(key); base != want[0] || suffix != want[1] {
			t.Errorf("splitSuffix(%q) = %q, %q", key, base, suffix)
		}
	}
}

func TestSolrDocumentRender(t *testing.T) {
	out := ansi.Strip(solrDocRender(solrFixture(t), true)(100))
	for _, want := range []string{
		"Fields that are searched but not stored",
		"Search debug shows what they match.",
		"depositor_{ssi,ssim,tesi,tesim,tsi,tsim}",
		"characters; r shows it whole", // the MODS XML, cut to a line
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "<mods") && len([]rune(l)) > 260 {
			t.Errorf("a long value should be cut: %d runes", len([]rune(l)))
		}
	}
	if strings.Contains(ansi.Strip(solrDocRender(solrFixture(t), false)(100)), "Search debug") {
		t.Error("a type with no Search debug shouldn't point at it")
	}
}

func TestSolrValueLine(t *testing.T) {
	if got := ansi.Strip(solrValueLine("a\r\nb")); got != "a⏎b" {
		t.Errorf("line breaks: %q", got)
	}
	if got := ansi.Strip(solrValueLine(" ")); got != "(blank)" {
		t.Errorf("blank: %q", got)
	}
	if got := ansi.Strip(solrValueLine(strings.Repeat("x", 12345))); !strings.HasSuffix(got, "… 12,345 characters; r shows it whole") {
		t.Errorf("long: %q", got[len(got)-50:])
	}
}

func TestValueLinesFoldRunsAndStopAtALimit(t *testing.T) {
	got := valueLines([]string{"Work", "Work", "Work", "public"})
	if len(got) != 2 || ansi.Strip(got[0]) != "Work  ×3" || ansi.Strip(got[1]) != "public" {
		t.Errorf("runs: %q", got)
	}
	many := make([]string, 20)
	for i := range many {
		many[i] = fmt.Sprint(i)
	}
	got = valueLines(many)
	if len(got) != solrValueRows || ansi.Strip(got[solrValueRows-1]) != "… 13 more; r shows them all" {
		t.Errorf("limit: %q", got)
	}
}
