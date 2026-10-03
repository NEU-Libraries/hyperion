package atlas

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestIndexReads(t *testing.T) {
	srv := fakeAtlas(t, nil,
		map[string]int{
			"/resources/gone/solr":             http.StatusNotFound,
			"/resources/w2/solr":               http.StatusForbidden,
			"/resources/s1/search_explanation": http.StatusNotFound,
			"/resources/w3/search_explanation": http.StatusBadGateway,
		},
		map[string]string{
			"/resources/gone/solr": `{"error":"not_indexed"}`,
			"/resources/w1/solr":   `{"noid":"w1","document":{"id":"u1","title_tsim":["Harbor"],"alternate_ids_ssim":["id-w1"]}}`,
			"/resources/w1/search_explanation": `{"noid":"w1","q":"harbor","matched":true,"score":10.5,"hidden_by":["featured"],
				"parsed_query":"+(title_tsim:harbor)","handler":{"qf":{"title_tsim":10},"pf":{},"mm":null,"tie":0.01,"boost":null},
				"explanation":{"match":true,"value":10.5,"description":"sum of:","details":[{"match":true,"value":10.5,"description":"weight(title_tsim:harbor in 3)"}]},
				"fields":[{"field":"title_tsim","stored":true,"query_tokens":{"typed":["harbor"],"analysed":["harbor"]},
				  "values":[{"source":"title_tsim","text":"Harbor","tokens":[{"text":"harbor","start":0,"end":6,"position":1,"match":true}]}],
				  "truncated":false,"reason":null,"highlights":[]}]}`,
			"/resources/w3/search_explanation": `{"error":"solr_unavailable"}`,
		})
	c, ctx := New(srv.URL, "tok"), context.Background()

	doc, err := c.SolrDocument(ctx, "w1")
	if err != nil || len(doc) != 3 || doc[0].Key != "id" || doc[1].Key != "title_tsim" {
		t.Errorf("solr document = %+v, %v; want its fields in Solr's order", doc, err)
	}
	if _, err := c.SolrDocument(ctx, "gone"); !errors.Is(err, ErrNotIndexed) {
		t.Errorf("unindexed document: err = %v, want ErrNotIndexed", err)
	}
	if _, err := c.SolrDocument(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown PID: err = %v, want ErrNotFound", err)
	}
	if _, err := c.SolrDocument(ctx, "w2"); !errors.Is(err, ErrForbidden) {
		t.Errorf("not an admin: err = %v, want ErrForbidden", err)
	}

	ex, err := c.ExplainSearch(ctx, "w1", "harbor")
	if err != nil {
		t.Fatal(err)
	}
	if !ex.Matched || ex.Score != 10.5 || ex.HiddenBy[0] != "featured" || ex.Handler.QF["title_tsim"] != 10 || ex.Handler.MM != nil {
		t.Errorf("explanation = %+v", ex)
	}
	if n := ex.Explanation.Details[0]; n.Description != "weight(title_tsim:harbor in 3)" || n.Value != 10.5 {
		t.Errorf("tree = %+v", ex.Explanation)
	}
	f := ex.Fields[0]
	if f.Reason != "" || f.QueryTokens.Analysed[0] != "harbor" || !f.Values[0].Tokens[0].Match || f.Values[0].Tokens[0].End != 6 {
		t.Errorf("field = %+v", f)
	}
	if _, err := c.ExplainSearch(ctx, "s1", "harbor"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a Set: err = %v, want ErrNotFound", err)
	}
	var he *HTTPError
	if _, err := c.ExplainSearch(ctx, "w3", "harbor"); !errors.As(err, &he) || he.Status != http.StatusBadGateway || he.Summary() != "solr_unavailable" {
		t.Errorf("Solr down: err = %v", err)
	}
}

func TestPathSearchExplanationEscapesTheWords(t *testing.T) {
	if got := PathSearchExplanation("w1", `"coastal" & surveys`); got != "/resources/w1/search_explanation?q=%22coastal%22+%26+surveys" {
		t.Errorf("path = %s", got)
	}
}

func TestReadsIndex(t *testing.T) {
	for _, c := range []struct {
		u    User
		want bool
	}{
		{User{Role: "admin"}, true},
		{User{Role: "privileged", Groups: []string{AdminGroup}}, true},
		{User{Role: "privileged"}, false},
		{User{Role: "standard", Groups: []string{AdminGroup}}, false},
	} {
		if got := c.u.ReadsIndex(); got != c.want {
			t.Errorf("%+v: ReadsIndex = %v", c.u, got)
		}
	}
}
