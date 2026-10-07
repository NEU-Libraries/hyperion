package atlas

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestSectionReads(t *testing.T) {
	srv := fakeAtlas(t, nil,
		map[string]int{
			"/resources/w1/mods/versions": http.StatusForbidden,
			"/works/w2/mets":              http.StatusNotFound,
			"/collections/gone/children":  http.StatusGone,
			"/resources/shut/children":    http.StatusForbidden,
		},
		map[string]string{
			"/collections/k1/children":       `["w1","w2"]`,
			"/communities/c1/children":       `["k1"]`,
			"/resources/gone/children":       `[{"id":"u1","noid":"w1","klass":"Work","title":"One","thumbnail":null,"tombstoned":true},{"noid":"k2","klass":"Collection","title":"Two","tombstoned":false}]`,
			"/compilations/s1/contents":      `{"contents":[{"noid":"w1","klass":"Work","title":"One"}],"pagination":{"pages":1}}`,
			"/files/b1/ancestry":             `{"file_set":"fs1","work":null}`,
			"/works/w1/mets":                 `{"work":{"id":"w1","mets":{"pages":["fs1"]}}}`,
			"/resources/w1/permissions":      `{"resource":{"read_groups":["public"],"embargo":null}}`,
			"/works/w1/associations":         `{"outbound":{"is_codebook_for":["w2"]},"inbound":{}}`,
			"/files/b1/versions":             `{"blob_id":"b1","versions":[{"revision":2,"version_id":"v4","size":10}]}`,
			"/works/w1/file_sets":            `[{"noid":"fs1","type":"Image","position":1,"tombstoned":false,"assets":[{"noid":"b1","size":10}]}]`,
			"/resources/w1/mods/versions/v2": `<mods/>`,
		})
	c, ctx := New(srv.URL, "tok"), context.Background()

	if got, err := c.Children(ctx, TypeCollection, "k1"); err != nil || len(got) != 2 {
		t.Errorf("collection children = %v, %v", got, err)
	}
	if got, err := c.Children(ctx, TypeCommunity, "c1"); err != nil || len(got) != 1 {
		t.Errorf("community children = %v, %v", got, err)
	}
	if _, err := c.Children(ctx, TypeWork, "w1"); err == nil {
		t.Error("a Work has no children route")
	}
	if _, err := c.Children(ctx, TypeCollection, "gone"); !errors.Is(err, ErrGone) {
		t.Errorf("tombstoned children: err = %v, want ErrGone", err)
	}
	if got, err := c.ResourceChildren(ctx, "gone"); err != nil || len(got) != 2 ||
		got[0] != (Digest{NOID: "w1", Klass: "Work", Title: "One", Tombstoned: true}) || got[1].Tombstoned {
		t.Errorf("resource children = %+v, %v", got, err)
	}
	if _, err := c.ResourceChildren(ctx, "shut"); !errors.Is(err, ErrForbidden) {
		t.Errorf("resource children without admin: err = %v, want ErrForbidden", err)
	}
	if got, err := c.SetContents(ctx, "s1"); err != nil || len(got) != 1 || got[0].Title != "One" || got[0].Tombstoned {
		t.Errorf("set contents = %+v, %v", got, err)
	}
	if got, err := c.FileAncestry(ctx, "b1"); err != nil || got != (FileAncestry{FileSet: "fs1"}) {
		t.Errorf("file ancestry = %+v, %v", got, err)
	}
	if got, err := c.METS(ctx, TypeWork, "w1"); err != nil || string(got) != `{"pages":["fs1"]}` {
		t.Errorf("mets = %s, %v", got, err)
	}
	if _, err := c.METS(ctx, TypeWork, "w2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("uncompleted mets: err = %v, want ErrNotFound", err)
	}
	if _, err := c.MODSVersions(ctx, "w1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("mods versions: err = %v, want ErrForbidden", err)
	}
	if got, err := c.MODSAt(ctx, "w1", "v2"); err != nil || string(got) != `<mods/>` {
		t.Errorf("mods at v2 = %s, %v", got, err)
	}
	if got, err := c.Permissions(ctx, "w1"); err != nil || len(got) != 2 || got[0].Key != "read_groups" {
		t.Errorf("permissions = %+v, %v", got, err)
	}
	if got, err := c.WorkAssociations(ctx, "w1"); err != nil || got.Outbound["is_codebook_for"][0] != "w2" {
		t.Errorf("associations = %+v, %v", got, err)
	}
	if got, err := c.FileVersions(ctx, "b1"); err != nil || len(got) != 1 || got[0].Revision != 2 || *got[0].Size != 10 {
		t.Errorf("file versions = %+v, %v", got, err)
	}
	if got, err := c.FileSets(ctx, "w1"); err != nil || len(got) != 1 || *got[0].Position != 1 || len(got[0].Assets) != 1 {
		t.Errorf("file sets = %+v, %v", got, err)
	}
}

func TestRawReturnsBodiesVerbatim(t *testing.T) {
	srv := fakeAtlas(t, nil,
		map[string]int{"/works/gone": http.StatusGone},
		map[string]string{
			"/works/w1":   `{"work":{"id":"w1"}}`,
			"/works/gone": `{"work":{"id":"gone","tombstoned":true}}`,
		})
	c, ctx := New(srv.URL, "tok"), context.Background()
	if got, err := c.Raw(ctx, "/works/w1"); err != nil || string(got) != `{"work":{"id":"w1"}}` {
		t.Errorf("raw = %s, %v", got, err)
	}
	if got, err := c.Raw(ctx, "/works/gone"); err != nil || len(got) == 0 {
		t.Errorf("a 410 body is still the answer: %s, %v", got, err)
	}
	if _, err := c.Raw(ctx, "/works/nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v", err)
	}
}

func TestPathsEscapeIDs(t *testing.T) {
	if got := PathMODSVersion("a/b", "v 1"); got != "/resources/a%2Fb/mods/versions/v%201" {
		t.Errorf("PathMODSVersion = %q", got)
	}
	if PathChildren(TypeWork, "w1") != "" || PathMETS(TypeCollection, "k1") != "" {
		t.Error("a route that doesn't exist for a type should be empty")
	}
}
