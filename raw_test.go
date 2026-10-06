package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NEU-Libraries/hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// sessionAgainst is an authenticated session whose client talks to a fake
// Atlas serving pages by path; it records every path requested.
func sessionAgainst(t *testing.T, pages map[string]string) (*session, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		body, ok := pages[r.URL.RequestURI()]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	sess := testSession()
	sess.client = atlas.New(srv.URL, "tok")
	return sess, &asked
}

func loadedPager(t *testing.T, s pagerScreen) pagerScreen {
	t.Helper()
	s, _ = sendPager(s, initResult[pagerMsg](t, s.Init()))
	return s
}

func TestRawViewShowsTheRoute(t *testing.T) {
	body := `["w1","w2"]`
	sess, asked := sessionAgainst(t, map[string]string{"/collections/k1/children": body})
	obj := atlas.Object{Type: atlas.TypeCollection, NOID: "k1", Title: "Maps"}

	list := childrenSection(sess, obj).(listScreen)
	if list.rawPath != atlas.PathChildren(atlas.TypeCollection, "k1") {
		t.Fatalf("children rawPath = %q", list.rawPath)
	}
	list, _ = sendList(list, initResult[listMsg](t, list.Init()))
	if !strings.Contains(list.View(), "r raw") {
		t.Errorf("list footer should offer r:\n%s", list.View())
	}
	_, cmd := sendList(list, typed("r"))
	raw := loadedPager(t, cmd().(pushMsg).s.(pagerScreen))

	v := ansi.Strip(raw.View())
	for _, want := range []string{"Children (raw)", "GET /collections/k1/children", `"w1"`, "r back", "s save"} {
		if !strings.Contains(v, want) {
			t.Errorf("raw view missing %q:\n%s", want, v)
		}
	}
	if string(raw.raw) != body {
		t.Errorf("raw bytes = %q, want Atlas's body verbatim", raw.raw)
	}
	if strings.Join(*asked, ",") != "/collections/k1/children,/collections/k1/children" {
		t.Errorf("requests = %v (one for the list, one for the raw view)", *asked)
	}
	if _, cmd := sendPager(raw, typed("r")); !isPop(cmd) {
		t.Error("r on the raw view should go back")
	}
}

func TestOverviewRawUsesTheRecordInHand(t *testing.T) {
	sess, asked := sessionAgainst(t, nil)
	record := `{"work":{"id":"w1","title":"Harbor"}}`
	s := newObjectScreen(sess, "w1")
	next, _ := s.Update(objectMsg{id: s.id, obj: atlas.Object{
		Type: atlas.TypeWork, NOID: "w1", Title: "Harbor", Path: "/works/w1", Raw: []byte(record),
		Fields: []atlas.Field{{Key: "id", Value: []byte(`"w1"`)}},
	}})
	if !strings.Contains(next.View(), "r raw") {
		t.Errorf("overview footer should offer r:\n%s", next.View())
	}
	_, cmd := next.Update(typed("r"))
	raw := loadedPager(t, cmd().(pushMsg).s.(pagerScreen))
	if !strings.Contains(ansi.Strip(raw.View()), "GET /works/w1") || string(raw.raw) != record {
		t.Errorf("raw record view:\n%s", raw.View())
	}
	if len(*asked) != 0 {
		t.Errorf("the record was in hand; requests = %v", *asked)
	}
}

func TestSectionsOfferRawAndSaveWhereTheyShould(t *testing.T) {
	sess, _ := sessionAgainst(t, map[string]string{
		"/resources/w1/mods.xml":    "<mods/>",
		"/resources/w1/permissions": `{"resource":{"read_groups":["public"]}}`,
	})
	obj := atlas.Object{Type: atlas.TypeWork, NOID: "w1", Title: "Harbor", Path: "/works/w1", Raw: []byte(`{}`)}

	mods := loadedPager(t, modsSection(sess, obj).(pagerScreen))
	if mods.rawPath != "/resources/w1/mods.xml" || string(mods.raw) != "<mods/>" || !strings.Contains(mods.View(), "s save") {
		t.Errorf("MODS: rawPath=%q raw=%q\n%s", mods.rawPath, mods.raw, mods.View())
	}
	perms := loadedPager(t, permissionsSection(sess, obj).(pagerScreen))
	if perms.rawPath != "/resources/w1/permissions" || perms.raw != nil || strings.Contains(perms.View(), "s save") {
		t.Errorf("Permissions shows fields, so r but no s: rawPath=%q raw=%q", perms.rawPath, perms.raw)
	}
	record := loadedPager(t, recordSection(sess, obj).(pagerScreen))
	if record.rawPath != "/works/w1" || string(record.rawBody) != "{}" {
		t.Errorf("Record: rawPath=%q rawBody=%q", record.rawPath, record.rawBody)
	}
}

func TestSaveWritesExactBytesAndNeverOverwrites(t *testing.T) {
	const mods = "<mods>\n\t<title>x</title>\n</mods>\n"
	sess, _ := sessionAgainst(t, map[string]string{"/resources/w1/mods.xml": mods})
	obj := atlas.Object{Type: atlas.TypeWork, NOID: "w1"}
	s := loadedPager(t, modsSection(sess, obj).(pagerScreen))

	s, _ = sendPager(s, typed("s"))
	if !s.save.active || s.save.input.Value() != "./w1-mods.xml" {
		t.Fatalf("s should open the prompt with ./w1-mods.xml, got active=%v %q", s.save.active, s.save.input.Value())
	}
	if v := ansi.Strip(s.View()); !strings.Contains(v, "Save to: ./w1-mods.xml") || !strings.Contains(v, "enter to save") {
		t.Errorf("prompt view:\n%s", v)
	}

	path := filepath.Join(t.TempDir(), "harbor.xml")
	s.save.input.SetValue(path)
	s, _ = sendPager(s, enter)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != mods {
		t.Fatalf("saved %q, %v — want Atlas's bytes, tabs and all", got, err)
	}
	if s.save.active || !strings.Contains(s.View(), fmt.Sprintf("Saved %d bytes to %s", len(mods), path)) {
		t.Errorf("after saving:\n%s", s.View())
	}

	s, _ = sendPager(s, typed("s"))
	s.save.input.SetValue(path)
	s, _ = sendPager(s, enter)
	if !s.save.active || !strings.Contains(s.View(), "already exists") {
		t.Errorf("saving over a file should refuse and keep the prompt:\n%s", s.View())
	}
	s.save.input.SetValue(filepath.Join(t.TempDir(), "no-such-dir", "x.xml"))
	s, _ = sendPager(s, enter)
	if !strings.Contains(s.View(), "doesn't exist") {
		t.Errorf("a missing folder should say so:\n%s", s.View())
	}
	s, _ = sendPager(s, esc)
	if s.save.active {
		t.Error("esc should cancel the prompt")
	}
	if _, cmd := sendPager(s, esc); !isPop(cmd) {
		t.Error("esc again should leave the pager")
	}
}

func TestSaveKeysGoToThePrompt(t *testing.T) {
	sess, _ := sessionAgainst(t, map[string]string{"/resources/w1/mods.xml": "<mods/>"})
	s := loadedPager(t, modsSection(sess, atlas.Object{Type: atlas.TypeWork, NOID: "w1"}).(pagerScreen))
	s, _ = sendPager(s, typed("s"))
	for _, k := range []tea.KeyMsg{typed("q"), typed("w"), typed("r")} {
		var cmd tea.Cmd
		s, cmd = sendPager(s, k)
		if isPop(cmd) || !s.save.active {
			t.Fatalf("%q while typing a name should type, not act", k.String())
		}
	}
	if !strings.HasSuffix(s.save.input.Value(), "qwr") || sess.wrap {
		t.Errorf("typed name = %q, wrap = %v", s.save.input.Value(), sess.wrap)
	}
}

func TestExtensionFor(t *testing.T) {
	for body, want := range map[string]string{`{"a":1}`: ".json", "  <mods/>": ".xml", "plain": ".txt"} {
		if got := extensionFor([]byte(body)); got != want {
			t.Errorf("extensionFor(%q) = %q, want %q", body, got, want)
		}
	}
}
