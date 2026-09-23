package atlas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPathSearchEscapesTheText(t *testing.T) {
	path := PathSearch(`moby "dick" & co #1`, "", 2, 100)
	u, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/resources/search" {
		t.Errorf("path = %q", u.Path)
	}
	q := u.Query()
	if got := q.Get("q"); got != `moby "dick" & co #1` {
		t.Errorf("q = %q", got)
	}
	if q.Has("type") {
		t.Errorf("an empty type should be left out: %s", path)
	}
	if q.Get("page") != "2" || q.Get("per_page") != "100" {
		t.Errorf("paging = %s", path)
	}
	if got := PathSearch("", TypePerson, 1, 100); got != "/resources/search?page=1&per_page=100&type=Person" {
		t.Errorf("type-only search = %q", got)
	}
}

func TestSearchDecodesAPage(t *testing.T) {
	var asked url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query()
		_, _ = w.Write([]byte(`{"results":[
			{"id":"t4b8k7m","noid":"t4b8k7m","klass":"Work","title":"Sample Presentation","creators":["Hopper, Grace"],
			 "year":"2026","thumbnail":null,"in_progress":false,"embargoed":true,"incomplete":false},
			{"id":"p1","noid":"p1","klass":"Person","title":"Starbuck, Obed","year":null}],
			"pagination":{"total":312,"page":1,"per_page":100,"pages":4}}`))
	}))
	defer srv.Close()

	res, err := New(srv.URL, "tok").Search(context.Background(), "sample", TypeWork, 1)
	if err != nil {
		t.Fatal(err)
	}
	if asked.Get("q") != "sample" || asked.Get("type") != "Work" || asked.Get("per_page") != "100" {
		t.Errorf("query sent = %v", asked)
	}
	if len(res.Results) != 2 || res.Pagination.Total != 312 || res.Pagination.Pages != 4 {
		t.Fatalf("page = %+v", res)
	}
	w, p := res.Results[0], res.Results[1]
	if w.NOID != "t4b8k7m" || w.Klass != "Work" || w.Title != "Sample Presentation" || !w.Embargoed ||
		len(w.Creators) != 1 || w.Year == nil || *w.Year != "2026" {
		t.Errorf("work hit = %+v", w)
	}
	if p.Year != nil || p.Creators != nil {
		t.Errorf("a null year and missing creators should decode empty: %+v", p)
	}
}
