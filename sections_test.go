package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
)

// initResult runs a screen's Init batch — its spinner tick and its load — and
// returns the load's result.
func initResult[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		batch = tea.BatchMsg{func() tea.Msg { return msg }}
	}
	for _, c := range batch {
		if m, ok := c().(T); ok {
			return m
		}
	}
	var zero T
	t.Fatalf("Init produced no %T", zero)
	return zero
}

func labelsOf(ss []section) string {
	l := make([]string, len(ss))
	for i, s := range ss {
		l[i] = s.label
	}
	return strings.Join(l, ", ")
}

func TestSectionsForEachType(t *testing.T) {
	anc := []atlas.Ancestor{{NOID: "c0", Klass: "Community"}}
	for _, c := range []struct {
		obj  atlas.Object
		want string
	}{
		{atlas.Object{Type: atlas.TypeWork, Ancestors: anc},
			"Record, MODS, MODS history, METS, FileSets, Downloadable files, Linked collections, Associations, Ancestors, Permissions"},
		{atlas.Object{Type: atlas.TypeCollection, Ancestors: anc},
			"Record, MODS, MODS history, Children, Ancestors, Permissions"},
		{atlas.Object{Type: atlas.TypeCommunity}, "Record, MODS, MODS history, Children, Permissions"},
		{atlas.Object{Type: atlas.TypeSet, Fields: []atlas.Field{{Key: "included_works", Value: []byte(`["w1"]`)}}},
			"Record, Contents, Set definition"},
		{atlas.Object{Type: atlas.TypeFileSet}, "Record, METS, Permissions"},
		{atlas.Object{Type: atlas.TypeFile}, "Record, Versions, FileSet and Work, Permissions"},
		{atlas.Object{Type: atlas.TypeDelegate}, "Record, Permissions"},
		{atlas.Object{Type: atlas.TypePerson, Fields: []atlas.Field{{Key: "personal_root_id", Value: []byte(`"k9"`)}}},
			"Record, Referenced PIDs, Permissions"},
	} {
		if got := labelsOf(sectionsFor(c.obj, nil)); got != c.want {
			t.Errorf("%s: sections = %s\n want %s", c.obj.Type, got, c.want)
		}
	}
}

func TestReferencedItems(t *testing.T) {
	obj := atlas.Object{Type: atlas.TypeSet, Fields: []atlas.Field{
		{Key: "included_collections", Value: []byte(`["k1","k2"]`)},
		{Key: "excluded_works", Value: []byte(`[]`)},
		{Key: "personal_root_id", Value: []byte(`null`)},
	}}
	items := referencedItems(obj)
	if len(items) != 2 || items[0].pid != "k1" || items[0].note != "Included collections" || !items[0].lookup {
		t.Errorf("items = %+v", items)
	}
}

func TestFileSetFromWorkCarriesItsFiles(t *testing.T) {
	sess := testSession()
	pos := 2
	size := int64(4096)
	sets := []atlas.FileSetEntry{{NOID: "fs1", Type: "Image", Position: &pos, Assets: []atlas.Asset{
		{NOID: "b1", OriginalFilename: "page2.tif", Size: &size, Role: "original_file", MimeType: "image/tiff"},
		{NOID: "d1", URI: "https://iiif.example/d1", Use: "Thumbnail", Gated: true},
	}}}
	list := staticList(sess, "FileSets", "", "", nil)
	list.state, list.items = loaded, fileSetItems(sets)

	if row := list.row(list.items[0], false); !strings.Contains(row, "Image FileSet  fs1 · position 2 · 2 files") {
		t.Errorf("fileset row = %q", row)
	}
	_, cmd := list.Update(enter)
	fso, ok := cmd().(pushMsg).s.(objectScreen)
	if !ok || fso.pid != "fs1" {
		t.Fatalf("enter should open FileSet fs1, got %T", cmd().(pushMsg).s)
	}
	secs := sectionsFor(atlas.Object{Type: atlas.TypeFileSet}, fso.extra)
	if got := labelsOf(secs); got != "Record, METS, Files, Permissions" {
		t.Fatalf("sections = %s", got)
	}
	files := secs[2].open(sess, atlas.Object{Type: atlas.TypeFileSet, NOID: "fs1"}).(listScreen)
	files, _ = sendList(files, initResult[listMsg](t, files.Init()))
	v := files.View()
	for _, want := range []string{"page2.tif", "File · b1 · Original file · image/tiff · 4.0 KiB",
		"Thumbnail (IIIF image)  Delegate · d1 · https://iiif.example/d1", "gated"} {
		if !strings.Contains(v, want) {
			t.Errorf("files view missing %q:\n%s", want, v)
		}
	}
}

func sendList(s listScreen, msg tea.Msg) (listScreen, tea.Cmd) {
	next, cmd := s.Update(msg)
	return next.(listScreen), cmd
}

func TestListLooksUpOnlyVisibleRows(t *testing.T) {
	sess := testSession()
	sess.width, sess.height = 80, chromeLines+3 // room for three rows
	s := staticList(sess, "Children", "", "", pidItems([]string{"a", "b", "c", "d", "e", "f"}, ""))
	s, _ = sendList(s, initResult[listMsg](t, s.Init()))
	if len(s.requested) != 3 || !s.requested[0] || !s.requested[2] || s.requested[3] {
		t.Fatalf("after load, requested = %v, want rows 0-2", s.requested)
	}

	s, _ = sendList(s, lookupMsg{id: s.id, idx: 1, obj: atlas.Object{Type: atlas.TypeWork, Title: "Harbor",
		Fields: []atlas.Field{{Key: "tombstoned", Value: []byte("true")}}}})
	s, _ = sendList(s, lookupMsg{id: s.id, idx: 2, err: atlas.ErrForbidden})
	s, _ = sendList(s, lookupMsg{id: s.id + 1000, idx: 0, obj: atlas.Object{Title: "stale"}})
	v := s.View()
	for _, want := range []string{"Harbor", "Work · b", "tombstoned", "not readable", "1/6"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "stale") {
		t.Error("a lookup for another screen was applied")
	}

	for range 4 { // move to row 4: the window scrolls and rows 3-4 get looked up
		s, _ = sendList(s, tea.KeyMsg{Type: tea.KeyDown})
	}
	if s.offset != 2 || !s.requested[3] || !s.requested[4] || s.requested[5] {
		t.Errorf("after scrolling: offset = %d requested = %v", s.offset, s.requested)
	}
	s, _ = sendList(s, tea.KeyMsg{Type: tea.KeyEnd})
	if s.cursor != 5 || s.offset != 3 {
		t.Errorf("end: cursor = %d offset = %d", s.cursor, s.offset)
	}
}

func TestListEmptyAndErrors(t *testing.T) {
	sess := testSession()
	empty := newListScreen(sess, "Children", "", "Nothing is directly beneath this Collection.",
		func(context.Context, *atlas.Client) ([]listItem, error) { return nil, nil })
	empty, _ = sendList(empty, initResult[listMsg](t, empty.Init()))
	if !strings.Contains(empty.View(), "Nothing is directly beneath") {
		t.Errorf("empty view:\n%s", empty.View())
	}
	if _, cmd := sendList(empty, enter); cmd != nil {
		t.Error("enter on an empty list should do nothing")
	}

	gone := newListScreen(sess, "Children", "", "", func(context.Context, *atlas.Client) ([]listItem, error) {
		return nil, atlas.ErrGone
	})
	gone, _ = sendList(gone, initResult[listMsg](t, gone.Init()))
	if !strings.Contains(gone.View(), "tombstoned") {
		t.Errorf("gone view:\n%s", gone.View())
	}
}

func sendPager(s pagerScreen, msg tea.Msg) (pagerScreen, tea.Cmd) {
	next, cmd := s.Update(msg)
	return next.(pagerScreen), cmd
}

func TestPagerScrolls(t *testing.T) {
	sess := testSession()
	sess.width, sess.height = 40, chromeLines+3
	lines := []string{"one", "two", "three", "four", "five"}
	s := staticPager(sess, "MODS", "Work w1 · Harbor", textRender(strings.Join(lines, "\n")+"\n\n"))
	s, _ = sendPager(s, initResult[pagerMsg](t, s.Init()))
	if v := s.View(); !strings.Contains(v, "three") || strings.Contains(v, "four") || !strings.Contains(v, "0%") {
		t.Errorf("first page:\n%s", v)
	}
	s, _ = sendPager(s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if v := s.View(); !strings.Contains(v, "five") || strings.Contains(v, "one") {
		t.Errorf("after G:\n%s", v)
	}
	if _, cmd := sendPager(s, esc); !isPop(cmd) {
		t.Error("esc should go back")
	}
}

func TestPagerErrors(t *testing.T) {
	for err, want := range map[error]string{
		userErr("MODS history is admin only: it names who made each edit."): "admin only",
		atlas.ErrForbidden: "This token may not read it.",
		errors.New("boom"): "boom",
	} {
		s := newPagerScreen(testSession(), "MODS", "", func(context.Context, *atlas.Client) (doc, error) {
			return doc{}, err
		})
		s, _ = sendPager(s, initResult[pagerMsg](t, s.Init()))
		if !strings.Contains(s.View(), want) {
			t.Errorf("%v: view = %q", err, s.View())
		}
	}
}

func TestFieldsRenderWraps(t *testing.T) {
	out := fieldsRender([]atlas.Field{
		{Key: "id", Value: []byte(`"w1"`)},
		{Key: "description", Value: []byte(`"alpha beta gamma delta epsilon"`)},
	})(30)
	lines := strings.Split(out, "\n")
	if len(lines) < 3 || !strings.Contains(lines[0], "PID") || !strings.HasPrefix(lines[2], strings.Repeat(" ", len("Description")+2)) {
		t.Errorf("fields render:\n%s", out)
	}
	if !strings.Contains(out, "epsilon") {
		t.Errorf("wrapped value lost text:\n%s", out)
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := byNUID("001", "002"); got != "by NUID 001 for 002" {
		t.Errorf("byNUID = %q", got)
	}
	if got := joinNonEmpty("a", "", " ", "b"); got != "a · b" {
		t.Errorf("joinNonEmpty = %q", got)
	}
	if got := plural(1, "file") + "/" + plural(3, "file"); got != "1 file/3 files" {
		t.Errorf("plural = %q", got)
	}
}
