package atlas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const workJSON = `{"work":{"id":"w1","valkyrie_id":"abc-123",
 "ancestors":[{"noid":"c0","klass":"Community","title":"Library"},{"noid":"k1","klass":"Collection","title":"Maps"}],
 "title":"Boston Harbor, 1775","permanent_url":"http://hdl.handle.net/2047/w1",
 "tombstoned":false,"depositor":"001234567"}}`

// fakeAtlas stands in for Atlas: /resources/:id redirects per redirects, and
// every other path answers from pages. Unlisted paths 404. It fails the test on
// any request that isn't an authenticated GET.
func fakeAtlas(t *testing.T, redirects map[string]string, statuses map[string]int, pages map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s: hyperion must only GET", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s: missing bearer token", r.URL.Path)
		}
		if code, ok := statuses[r.URL.Path]; ok {
			w.WriteHeader(code)
			if body, ok := pages[r.URL.Path]; ok {
				_, _ = w.Write([]byte(body))
			}
			return
		}
		if loc, ok := redirects[r.URL.Path]; ok {
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusFound)
			return
		}
		if body, ok := pages[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInspectFollowsRedirectToTypedRoute(t *testing.T) {
	// The Location names another host, as a proxy-confused Rails might; the
	// client must still read the typed path from its own base URL.
	srv := fakeAtlas(t,
		map[string]string{"/resources/w1": "http://elsewhere.invalid/works/w1"},
		nil,
		map[string]string{"/works/w1": workJSON})

	obj, err := New(srv.URL, "tok").Inspect(context.Background(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Type != TypeWork || obj.NOID != "w1" || obj.Path != "/works/w1" {
		t.Errorf("type=%q noid=%q path=%q", obj.Type, obj.NOID, obj.Path)
	}
	if obj.Title != "Boston Harbor, 1775" {
		t.Errorf("title = %q", obj.Title)
	}
	if len(obj.Ancestors) != 2 || obj.Ancestors[1] != (Ancestor{NOID: "k1", Klass: "Collection", Title: "Maps"}) {
		t.Errorf("ancestors = %+v", obj.Ancestors)
	}
	var keys []string
	for _, f := range obj.Fields {
		keys = append(keys, f.Key)
	}
	if got := strings.Join(keys, ","); got != "id,valkyrie_id,ancestors,title,permanent_url,tombstoned,depositor" {
		t.Errorf("field order = %s", got)
	}
	if string(obj.Raw) != workJSON {
		t.Error("raw body not kept verbatim")
	}
}

func TestInspectKeepsBasePathPrefix(t *testing.T) {
	srv := fakeAtlas(t,
		map[string]string{"/api/resources/b1": "/api/files/b1"},
		nil,
		map[string]string{"/api/files/b1": `{"blob":{"id":"b1","original_filename":"scan.tif","size":2048}}`})

	obj, err := New(srv.URL+"/api/", "tok").Inspect(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Type != TypeFile || obj.Title != "scan.tif" {
		t.Errorf("type=%q title=%q", obj.Type, obj.Title)
	}
}

func TestInspectFallsBackToCompilation(t *testing.T) {
	srv := fakeAtlas(t, nil, nil,
		map[string]string{"/compilations/s1": `{"compilation":{"id":"s1","title":"Reading list"}}`})

	obj, err := New(srv.URL, "tok").Inspect(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Type != TypeSet || obj.Title != "Reading list" {
		t.Errorf("type=%q title=%q", obj.Type, obj.Title)
	}
}

func TestInspectErrors(t *testing.T) {
	srv := fakeAtlas(t,
		map[string]string{"/resources/odd": "/somethings/odd"},
		map[string]int{"/resources/locked": http.StatusForbidden, "/resources/badtok": http.StatusUnauthorized},
		nil)
	c := New(srv.URL, "tok")

	for pid, want := range map[string]error{
		"missing": ErrNotFound, // 404 on /resources and on /compilations
		"locked":  ErrForbidden,
		"badtok":  ErrUnauthorized,
	} {
		if _, err := c.Inspect(context.Background(), pid); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", pid, err, want)
		}
	}
	if _, err := c.Inspect(context.Background(), "odd"); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("odd: err = %v, want unknown type", err)
	}
}

func TestInspectEscapesPID(t *testing.T) {
	srv := fakeAtlas(t, nil, nil, nil)
	_, err := New(srv.URL, "tok").Inspect(context.Background(), "../user")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound (the PID must not escape its path segment)", err)
	}
}

func TestObjectTitleFallbacks(t *testing.T) {
	for body, want := range map[string]string{
		`{"person":{"id":"p1","display_name":"Doe, Jane"}}`: "Doe, Jane",
		`{"file_set":{"id":"f1","type":"image"}}`:           "(untitled)",
		`{"work":{"id":"w1","title":"  "}}`:                 "(untitled)",
	} {
		obj, err := parseObject([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if obj.Title != want {
			t.Errorf("%s: title = %q, want %q", body, obj.Title, want)
		}
	}
}

func TestInspectReadsTombstonedRecord(t *testing.T) {
	// Atlas answers a tombstoned resource 410 with its full record.
	srv := fakeAtlas(t,
		map[string]string{"/resources/w9": "/works/w9"},
		map[string]int{"/works/w9": http.StatusGone},
		map[string]string{"/works/w9": `{"work":{"id":"w9","title":"Withdrawn","tombstoned":true}}`})

	obj, err := New(srv.URL, "tok").Inspect(context.Background(), "w9")
	if err != nil {
		t.Fatalf("a tombstoned record should still inspect: %v", err)
	}
	if obj.Title != "Withdrawn" {
		t.Errorf("title = %q", obj.Title)
	}
}
