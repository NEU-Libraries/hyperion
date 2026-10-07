package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	workRecord = `{"work":{"id":"w1","title":"Harbor","tombstoned":false}}`
	workMODS   = "<mods><titleInfo><title>Harbor</title></titleInfo></mods>"
	searchBody = `{"results":[{"noid":"w1","klass":"Work","title":"Harbor"}],"pagination":{"total":1,"page":1,"per_page":100,"pages":1}}`
)

// fakeAtlasCLI serves a small repository: Work w1, Collection k1 holding it,
// a Blob b1 whose resolver 500s, and a MODS version v2 of w1.
func fakeAtlasCLI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.RequestURI() {
		case "/user":
			w.Write([]byte(`{"nuid":"000000002","name":"Doe, Jane"}`))
		case "/resources/w1":
			w.Header().Set("Location", "/works/w1")
			w.WriteHeader(http.StatusFound)
		case "/resources/k1":
			w.Header().Set("Location", "/collections/k1")
			w.WriteHeader(http.StatusFound)
		case "/works/w1":
			w.Write([]byte(workRecord))
		case "/collections/k1":
			w.Write([]byte(`{"collection":{"id":"k1","title":"Maps"}}`))
		case "/compilations/s1":
			w.Write([]byte(`{"compilation":{"id":"s1","title":"Reading list"}}`))
		case "/collections/k1/children":
			w.Write([]byte(`["w1"]`))
		case "/resources/k1/children":
			w.Write([]byte(`[{"noid":"w1","klass":"Work","title":"Harbor","tombstoned":true}]`))
		case "/resources/k9":
			w.Header().Set("Location", "/collections/k9")
			w.WriteHeader(http.StatusFound)
		case "/collections/k9":
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(`{"collection":{"id":"k9","title":"Withdrawn","tombstoned":true}}`))
		case "/resources/k9/children":
			w.WriteHeader(http.StatusForbidden)
		case "/resources/w1/mods.xml":
			w.Write([]byte(workMODS))
		case "/resources/w1/mods":
			w.Write([]byte(`{"title":"Harbor"}`))
		case "/resources/w1/solr":
			w.Write([]byte(`{"noid":"w1","document":{"id":"u1"}}`))
		case "/resources/k1/solr":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not_indexed"}`))
		case "/resources/w1/search_explanation?q=harbor+maps":
			w.Write([]byte(`{"noid":"w1","matched":true}`))
		case "/resources/k1/search_explanation?q=secret":
			w.WriteHeader(http.StatusForbidden)
		case "/resources/w1/mods/versions/v2":
			w.Write([]byte("<mods>v2</mods>\n"))
		case "/resources/search?page=1&per_page=100&q=boston+harbor":
			w.Write([]byte(searchBody))
		case "/resources/search?page=2&per_page=100&q=harbor&type=Person":
			w.Write([]byte(`{"results":[],"pagination":{"total":0,"page":2,"per_page":100,"pages":0}}`))
		case "/resources/search?page=1&per_page=100&q=secret":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"not allowed"}`))
		case "/resources/b1":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"status":500,"exception":"#<NoMethodError: undefined method 'blob_url'>"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCLI runs hyperion with args against srv, returning status, stdout, stderr.
func runCLI(t *testing.T, srv *httptest.Server, terminal bool, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"ATLAS_URL": srv.URL, "ATLAS_JWT": "tok"}
	c := cli{stdout: &out, stderr: &errOut, getenv: func(k string) string { return env[k] }, stdoutIsTerminal: terminal}
	code := c.run(args)
	return code, out.String(), errOut.String()
}

func TestGetPrintsRawResponses(t *testing.T) {
	srv := fakeAtlasCLI(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "w1"}, workRecord},
		{[]string{"get", "w1", "record"}, workRecord},
		{[]string{"get", "w1", "mods"}, workMODS},
		{[]string{"get", "w1", "mods", "--at", "v2"}, "<mods>v2</mods>\n"},
		{[]string{"get", "--at", "v2", "w1", "mods"}, "<mods>v2</mods>\n"},
		{[]string{"get", "k1", "children"}, `["w1"]`},
		{[]string{"get", "k1", "children", "--all"}, `[{"noid":"w1","klass":"Work","title":"Harbor","tombstoned":true}]`},
		{[]string{"get", "w1", "mods-json"}, `{"title":"Harbor"}`},
		{[]string{"get", "w1", "solr"}, `{"noid":"w1","document":{"id":"u1"}}`},
		{[]string{"get", "w1", "search-debug", "--q", "harbor maps"}, `{"noid":"w1","matched":true}`},
	} {
		code, out, errOut := runCLI(t, srv, false, c.args...)
		if code != exitOK || out != c.want || errOut != "" {
			t.Errorf("%v: code=%d out=%q stderr=%q, want %q", c.args, code, out, errOut, c.want)
		}
	}
}

func TestGetAddsANewlineOnlyForATerminal(t *testing.T) {
	srv := fakeAtlasCLI(t)
	if _, out, _ := runCLI(t, srv, true, "get", "w1"); out != workRecord+"\n" {
		t.Errorf("terminal: %q", out)
	}
	if _, out, _ := runCLI(t, srv, true, "get", "w1", "mods", "--at", "v2"); out != "<mods>v2</mods>\n" {
		t.Errorf("a body ending in a newline gets no second one: %q", out)
	}
	if _, out, _ := runCLI(t, srv, false, "get", "w1"); out != workRecord {
		t.Errorf("piped output must be byte-exact: %q", out)
	}
}

func TestGetFailures(t *testing.T) {
	srv := fakeAtlasCLI(t)
	for _, c := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"get", "nope"}, exitNotFound, "No object has this PID"},
		{[]string{"get", "nope", "mods"}, exitNotFound, "No object has this PID"},
		{[]string{"get", "w1", "mets"}, exitFailed, "PID w1: this Work has no METS."},
		{[]string{"get", "k1", "mets"}, exitUsage, "PID k1 is a Collection; mets is only for a Work or FileSet."},
		{[]string{"get", "b1"}, exitFailed, "NoMethodError: undefined method 'blob_url'"},
		{[]string{"get", "w1", "children"}, exitUsage, "PID w1 is a Work; children is only for a Collection or Community."},
		{[]string{"get", "w1", "thumbnails"}, exitUsage, `no section "thumbnails"`},
		{[]string{"get", "w1", "files", "--at", "v2"}, exitUsage, "--at goes with the mods section"},
		{[]string{"get", "w1", "mods", "--all"}, exitUsage, "--all goes with the children section"},
		{[]string{"get", "w1", "children", "--all"}, exitUsage, "PID w1 is a Work; children is only for a Collection or Community."},
		{[]string{"get", "k9", "children", "--all"}, exitFailed, "children --all is only available to admins and delegated admins."},
		{[]string{"get", "w1", "mods", "--at", "v9"}, exitFailed,
			"PID w1: this Work has no MODS version v9. hyperion get w1 mods-history lists the versions Atlas keeps."},
		{[]string{"get", "s1", "contents", "--page", "0"}, exitUsage, "--page counts from 1"},
		{[]string{"get", "k1", "solr"}, exitFailed, "PID k1: Solr holds no document for it"},
		{[]string{"get", "k1", "search-debug", "--q", "secret"}, exitFailed, "only available to admins and delegated admins"},
		{[]string{"get", "s1", "search-debug", "--q", "x"}, exitUsage,
			"PID s1 is a Set; search-debug is only for a Work, Collection or Community."},
		{[]string{"get", "w1", "search-debug"}, exitUsage, "search-debug needs the words to explain, with --q"},
		{[]string{"get", "w1", "solr", "--q", "x"}, exitUsage, "--q goes with the search-debug section"},
		{[]string{"get"}, exitUsage, "Usage: hyperion get"},
		{[]string{"get", "a", "b", "c"}, exitUsage, "Usage: hyperion get"},
	} {
		code, out, errOut := runCLI(t, srv, false, c.args...)
		if code != c.code || out != "" || !strings.Contains(errOut, c.stderr) {
			t.Errorf("%v: code=%d stdout=%q stderr=%q, want code %d and %q", c.args, code, out, errOut, c.code, c.stderr)
		}
	}
}

func TestGetNeedsCredentials(t *testing.T) {
	var errOut bytes.Buffer
	c := cli{stdout: &bytes.Buffer{}, stderr: &errOut, getenv: func(string) string { return "" }}
	if code := c.run([]string{"get", "w1"}); code != exitUsage || !strings.Contains(errOut.String(), "no Atlas URL") {
		t.Errorf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestGetHelpListsEverySection(t *testing.T) {
	srv := fakeAtlasCLI(t)
	code, _, errOut := runCLI(t, srv, false, "get", "--help")
	if code != exitOK {
		t.Errorf("--help: code %d", code)
	}
	for _, s := range getSections {
		if !strings.Contains(errOut, "  "+s.name) {
			t.Errorf("help is missing section %q", s.name)
		}
	}
}

func TestExportBothForms(t *testing.T) {
	srv := fakeAtlasCLI(t)
	for _, args := range [][]string{
		{"--type", "collection", "--pid", "k1"},               // the older form
		{"export", "-t", "collection", "-p", "k1", "-c", "1"}, // the subcommand
	} {
		dir := filepath.Join(t.TempDir(), "out")
		code, _, errOut := runCLI(t, srv, false, append(args, "-o", dir)...)
		if code != exitOK {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut)
		}
		if got, err := os.ReadFile(filepath.Join(dir, "w1.xml")); err != nil || string(got) != workMODS {
			t.Errorf("%v: exported %q, %v", args, got, err)
		}
	}
	if code, _, errOut := runCLI(t, srv, false, "export", "-t", "collection"); code != exitUsage || !strings.Contains(errOut, "both required") {
		t.Errorf("export without --pid: code=%d stderr=%q", code, errOut)
	}
	if code, _, _ := runCLI(t, srv, false, "export", "-t", "collection", "-p", "zz", "-o", t.TempDir()); code != exitNotFound {
		t.Errorf("export of a missing collection: code %d, want %d", code, exitNotFound)
	}
}

func TestVersionAndStrayArguments(t *testing.T) {
	srv := fakeAtlasCLI(t)
	if code, out, _ := runCLI(t, srv, false, "--version"); code != exitOK || !strings.HasPrefix(out, "hyperion ") {
		t.Errorf("--version: code=%d out=%q", code, out)
	}
	if code, _, errOut := runCLI(t, srv, false, "w1"); code != exitUsage || !strings.Contains(errOut, `unexpected "w1"`) {
		t.Errorf("a bare PID: code=%d stderr=%q", code, errOut)
	}
}

func TestExportNamesTheWrongType(t *testing.T) {
	srv := fakeAtlasCLI(t)
	for args, want := range map[string]string{
		"export -t collection -p s1": "s1 is a Set, not a Collection. Go back and choose Set.",
		"export -t set -p w1":        "w1 is a Work. Only a Collection or a Set can be exported.",
	} {
		code, _, errOut := runCLI(t, srv, false, append(strings.Fields(args), "-o", t.TempDir())...)
		if code != exitFailed || !strings.Contains(errOut, want) {
			t.Errorf("%s: code=%d stderr=%q, want %q", args, code, errOut, want)
		}
	}
}

func TestSearchPrintsTheBodyUnchanged(t *testing.T) {
	srv := fakeAtlasCLI(t)
	code, out, errOut := runCLI(t, srv, false, "search", "boston", "harbor")
	if code != 0 || out != searchBody || errOut != "" {
		t.Errorf("search = %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, srv, false, "search", "harbor", "--type", "person", "--page", "2"); code != 0 || !strings.Contains(out, `"page":2`) {
		t.Errorf("a narrowed second page = %d %q", code, out)
	}
}

func TestSearchFailures(t *testing.T) {
	srv := fakeAtlasCLI(t)
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"search"}, 2, "Usage: hyperion search"},
		{[]string{"search", "harbor", "--type", "FileSet"}, 2, "--type must be one of Work, Collection, Community, Person"},
		{[]string{"search", "harbor", "--page", "0"}, 2, "--page counts from 1"},
		{[]string{"search", "secret"}, 1, "Couldn't search the catalog."},
	} {
		code, out, errOut := runCLI(t, srv, false, c.args...)
		if code != c.code || out != "" || !strings.Contains(errOut, c.msg) {
			t.Errorf("%v = %d %q %q; want %d and %q", c.args, code, out, errOut, c.code, c.msg)
		}
	}

	var out, errOut bytes.Buffer
	env := map[string]string{"ATLAS_URL": srv.URL, "ATLAS_JWT": "wrong"}
	cl := cli{stdout: &out, stderr: &errOut, getenv: func(k string) string { return env[k] }}
	if code := cl.run([]string{"search", "harbor"}); code != exitFailed || out.Len() != 0 {
		t.Errorf("a rejected token = %d %q %q", code, out.String(), errOut.String())
	}
}
