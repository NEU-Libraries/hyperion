package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/NEU-Libraries/hyperion/internal/atlas"
)

// section is one entry of an object's section menu; open builds its screen.
// group heads the menu's left column where a group starts. allowed, when set,
// says whether Atlas lets the user read the section; nil means anyone can.
type section struct {
	label   string
	group   string
	open    func(*session, atlas.Object) screen
	allowed func(atlas.User) bool
}

// modsSections are the metadata sections of every object that has MODS.
var modsSections = []section{
	{label: "MODS", open: modsSection},
	{label: "MODS JSON", open: modsJSONSection},
	{label: "MODS history", open: modsHistorySection, allowed: atlas.User.ReadsMODSHistory},
}

// sectionsFor lists the sections that apply to an object's type, grouped the
// same way for every type — the record, then its metadata, its content, its
// relationships, how search sees it, and who may see it — so a section is
// always found in the same place. extra carries sections the caller already has data for (a
// FileSet opened from its Work knows its files; Atlas can't list them alone).
func sectionsFor(obj atlas.Object, extra []section) []section {
	var meta, content, rel []section
	switch obj.Type {
	case atlas.TypeWork:
		meta = append(slices.Clone(modsSections), section{label: "METS", open: metsSection})
		content = []section{{label: "FileSets", open: fileSetsSection}, {label: "Downloadable files", open: filesSection}}
		rel = []section{{label: "Linked collections", open: linkedSection}, {label: "Associations", open: associationsSection}}
	case atlas.TypeCollection, atlas.TypeCommunity:
		meta = slices.Clone(modsSections)
		content = []section{{label: "Children", open: childrenSection}}
	case atlas.TypeSet:
		content = []section{{label: "Contents", open: contentsSection}}
	case atlas.TypeFileSet:
		meta = []section{{label: "METS", open: metsSection}}
	case atlas.TypeFile:
		content = []section{{label: "Versions", open: fileVersionsSection}}
		rel = []section{{label: "FileSet and Work", open: fileParentsSection}}
	}
	content = append(content, extra...)
	if len(obj.Ancestors) > 0 {
		rel = append(rel, section{label: "Ancestors", open: ancestorsSection})
	}
	if len(referencedItems(obj)) > 0 {
		label := "Referenced PIDs"
		if obj.Type == atlas.TypeSet {
			label = "Set definition" // its recipe; Contents is what the recipe resolves to
		}
		rel = append(rel, section{label: label, open: referencedSection})
	}

	var search []section
	if slices.Contains(atlas.IndexedTypes, obj.Type) {
		search = append(search, section{label: "Solr document", open: solrSection, allowed: atlas.User.ReadsIndex})
	}
	if slices.Contains(atlas.ExplainedTypes, obj.Type) {
		search = append(search, section{label: "Search debug", open: searchDebugSection, allowed: atlas.User.ReadsIndex})
	}

	ss := []section{{label: "Record", open: recordSection}}
	for _, g := range []struct {
		name string
		ss   []section
	}{{"Metadata", meta}, {"Content", content}, {"Relationships", rel}, {"Search", search}} {
		for _, sec := range g.ss {
			sec.group = g.name
			ss = append(ss, sec)
		}
	}
	if obj.Type != atlas.TypeSet { // a Compilation isn't a Resource; its ACL is in its record
		ss = append(ss, section{label: "Permissions", group: "Access", open: permissionsSection})
	}
	return ss
}

// modsHistoryLimit is why MODS history won't open for anyone but an admin or
// a delegated admin.
const modsHistoryLimit = "MODS history is only available to admins and delegated admins, because it shows who made each edit."

// objectContext is the dimmed line under a section's title naming its object.
func objectContext(obj atlas.Object) string {
	return fmt.Sprintf("%s %s · %s", obj.Type, obj.NOID, obj.Title)
}

func recordSection(sess *session, obj atlas.Object) screen {
	return staticPager(sess, "Record", objectContext(obj), fieldsRender(obj.Fields)).withRecord(obj)
}

func modsSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "MODS", objectContext(obj), func(ctx context.Context, c *atlas.Client) (doc, error) {
		xml, err := c.MODS(ctx, obj.NOID)
		if errors.Is(err, atlas.ErrNotFound) {
			return doc{}, notice("This " + string(obj.Type) + " has no MODS.")
		}
		return doc{render: xmlRender(string(xml)), raw: xml}, err
	}).withRaw(atlas.PathMODS(obj.NOID), stem(obj, "mods"))
}

func modsHistorySection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "MODS history", objectContext(obj), "No retained MODS versions.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			versions, err := c.MODSVersions(ctx, obj.NOID)
			if errors.Is(err, atlas.ErrForbidden) {
				return nil, notice(modsHistoryLimit)
			}
			items := make([]listItem, len(versions))
			for i, v := range versions {
				versionID := v.VersionID
				items[i] = listItem{
					title: versionID,
					note:  joinNonEmpty(when(v.Created), byNUID(v.ActorNUID, v.OnBehalfOfNUID), v.Source, v.Note),
					open: func(sess *session) screen {
						return newPagerScreen(sess, "MODS as of "+versionID, objectContext(obj),
							func(ctx context.Context, c *atlas.Client) (doc, error) {
								xml, err := c.MODSAt(ctx, obj.NOID, versionID)
								return doc{render: xmlRender(string(xml)), raw: xml}, err
							}).withRaw(atlas.PathMODSVersion(obj.NOID, versionID), stem(obj, "mods-"+versionID))
					},
				}
			}
			return items, err
		}).withRaw(atlas.PathMODSVersions(obj.NOID), stem(obj, "mods history"))
}

func metsSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "METS", objectContext(obj), func(ctx context.Context, c *atlas.Client) (doc, error) {
		mets, err := c.METS(ctx, obj.Type, obj.NOID)
		if errors.Is(err, atlas.ErrNotFound) {
			if obj.Type == atlas.TypeWork {
				return doc{}, notice("No METS yet: Atlas builds it when the Work is completed.")
			}
			return doc{}, notice("This FileSet has no METS.")
		}
		if err != nil {
			return doc{}, err
		}
		return doc{render: jsonRender(indentJSON(mets))}, nil
	}).withRaw(atlas.PathMETS(obj.Type, obj.NOID), stem(obj, "mets"))
}

func fileSetsSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "FileSets", objectContext(obj), "This Work has no FileSets.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			sets, err := c.FileSets(ctx, obj.NOID)
			return fileSetItems(sets), err
		}).withRaw(atlas.PathFileSets(obj.NOID), stem(obj, "filesets"))
}

// fileSetItems lists a Work's FileSets by position. Atlas has no route listing
// a FileSet's files, so each row hands the files this listing carries to the
// FileSet's own screen as an extra section.
func fileSetItems(sets []atlas.FileSetEntry) []listItem {
	items := make([]listItem, len(sets))
	for i, fs := range sets {
		// Named by its classification, like a FileSet's own screen.
		title := firstNonEmpty(fs.Type, "Unclassified") + " FileSet"
		position := ""
		if fs.Position != nil {
			position = fmt.Sprintf("position %d", *fs.Position)
		}
		it := listItem{pid: fs.NOID, title: title, note: joinNonEmpty(position, plural(len(fs.Assets), "file"))}
		if fs.Tombstoned {
			it.warn = tombstonedWarn
		}
		it.open = func(sess *session) screen {
			return newObjectScreen(sess, fs.NOID, section{label: "Files", open: func(sess *session, fso atlas.Object) screen {
				return staticList(sess, "Files", objectContext(fso), "This FileSet has no files.", assetItems(fs.Assets))
			}})
		}
		items[i] = it
	}
	return items
}

func filesSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Downloadable files", objectContext(obj), "This Work has no downloadable files.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			assets, err := c.Assets(ctx, obj.NOID)
			return assetItems(assets), err
		}).withRaw(atlas.PathAssets(obj.NOID), stem(obj, "files"))
}

// assetItems lists a Work's assets: a File by filename, a Delegate by URI.
func assetItems(assets []atlas.Asset) []listItem {
	items := make([]listItem, len(assets))
	for i, a := range assets {
		role := firstNonEmpty(a.Use, humanRole(a.Role))
		size := ""
		if a.Size != nil {
			size = humanBytes(float64(*a.Size))
		}
		it := listItem{pid: a.NOID, title: firstNonEmpty(a.OriginalFilename, a.Filename, role), kind: "File",
			note: joinNonEmpty(role, a.MimeType, size)}
		if a.Size == nil && a.URI != "" {
			// A Delegate holds no bytes, only an image server address: name it
			// by what it is for, and keep the address as detail.
			it.title, it.kind = firstNonEmpty(role, "Image")+" (IIIF image)", "Delegate"
			it.note = joinNonEmpty(a.MimeType, a.URI)
		}
		if a.Gated {
			it.warn = "gated"
		}
		items[i] = it
	}
	return items
}

func linkedSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Linked collections", objectContext(obj),
		"This Work is linked into no collections beyond its parent.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			noids, err := c.LinkedCollections(ctx, obj.NOID)
			return pidItems(noids, ""), err
		}).withRaw(atlas.PathLinkedMembers(obj.NOID), stem(obj, "linked collections"))
}

func associationsSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Associations", objectContext(obj), "This Work has no associations.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			as, err := c.WorkAssociations(ctx, obj.NOID)
			var items []listItem
			for _, p := range sortedKeys(as.Outbound) {
				items = append(items, pidItems(as.Outbound[p], "this Work "+predicate(p)+" it")...)
			}
			for _, p := range sortedKeys(as.Inbound) {
				items = append(items, pidItems(as.Inbound[p], "it "+predicate(p)+" this Work")...)
			}
			return items, err
		}).withRaw(atlas.PathAssociations(obj.NOID), stem(obj, "associations"))
}

func childrenSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Children", objectContext(obj), "Nothing is directly beneath this "+string(obj.Type)+".",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			noids, err := c.Children(ctx, obj.Type, obj.NOID)
			return pidItems(noids, ""), err
		}).withRaw(atlas.PathChildren(obj.Type, obj.NOID), stem(obj, "children"))
}

func contentsSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Contents", objectContext(obj), "This Set resolves to no works.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			digests, err := c.SetContents(ctx, obj.NOID)
			items := make([]listItem, len(digests))
			for i, d := range digests {
				items[i] = listItem{pid: d.NOID, title: d.Title, kind: d.Klass}
			}
			return items, err
		}).withRaw(atlas.PathSetContents(obj.NOID, 1), stem(obj, "contents"))
}

func fileParentsSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "FileSet and Work", objectContext(obj),
		"Atlas can't place this File under a FileSet or Work.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			a, err := c.FileAncestry(ctx, obj.NOID)
			var items []listItem
			if a.FileSet != "" {
				items = append(items, listItem{pid: a.FileSet, kind: "FileSet", lookup: true})
			}
			if a.Work != "" {
				items = append(items, listItem{pid: a.Work, kind: "Work", lookup: true})
			}
			return items, err
		}).withRaw(atlas.PathFileAncestry(obj.NOID), stem(obj, "ancestry"))
}

func fileVersionsSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Versions", objectContext(obj), "No retained versions.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			versions, err := c.FileVersions(ctx, obj.NOID)
			items := make([]listItem, len(versions))
			for i, v := range versions {
				size := ""
				if v.Size != nil {
					size = humanBytes(float64(*v.Size))
				}
				title := fmt.Sprintf("Revision %d", v.Revision)
				fields := structFields(v)
				items[i] = listItem{
					title: title,
					note:  joinNonEmpty(when(v.Created), byNUID(v.ActorNUID, v.OnBehalfOfNUID), size),
					open: func(sess *session) screen {
						return staticPager(sess, title, objectContext(obj), fieldsRender(fields)).
							withRaw(atlas.PathFileVersions(obj.NOID), stem(obj, "versions")).rawTitled("Versions")
					},
				}
			}
			return items, err
		}).withRaw(atlas.PathFileVersions(obj.NOID), stem(obj, "versions"))
}

func ancestorsSection(sess *session, obj atlas.Object) screen {
	items := make([]listItem, len(obj.Ancestors))
	for i, a := range obj.Ancestors {
		items[i] = listItem{pid: a.NOID, title: a.Title, kind: a.Klass, note: structuralLabel(a),
			inert: a.SystemContainer}
	}
	return staticList(sess, "Ancestors", objectContext(obj)+" · root first", "", items).withRecord(obj)
}

// referenceKeys are record fields that hold other objects' PIDs.
var referenceKeys = []string{"personal_root_id", "affiliated_community_ids",
	"included_collections", "included_works", "excluded_works"}

// referencedItems lists the PIDs an object's record points at (a Person's
// communities, a Set's recipe, …), each labelled with the field it came from.
func referencedItems(obj atlas.Object) []listItem {
	var items []listItem
	for _, key := range referenceKeys {
		for _, f := range obj.Fields {
			if f.Key != key {
				continue
			}
			var one string
			var many []string
			if json.Unmarshal(f.Value, &one) == nil && one != "" {
				many = []string{one}
			} else {
				_ = json.Unmarshal(f.Value, &many)
			}
			label := fieldLabel(key)
			label = strings.TrimSuffix(strings.TrimSuffix(label, " (PIDs)"), " (PID)")
			items = append(items, pidItems(many, label)...)
		}
	}
	return items
}

func referencedSection(sess *session, obj atlas.Object) screen {
	title := "Referenced PIDs"
	if obj.Type == atlas.TypeSet {
		title = "Set definition"
	}
	return staticList(sess, title, objectContext(obj), "", referencedItems(obj)).withRecord(obj)
}

func permissionsSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "Permissions", objectContext(obj), func(ctx context.Context, c *atlas.Client) (doc, error) {
		fields, err := c.Permissions(ctx, obj.NOID)
		if err != nil {
			return doc{}, err
		}
		// Atlas repeats the object's type in the envelope; the title says it.
		kept := fields[:0:0]
		for _, f := range fields {
			if f.Key != "type" {
				kept = append(kept, f)
			}
		}
		return doc{render: fieldsRender(kept)}, nil
	}).withRaw(atlas.PathPermissions(obj.NOID), stem(obj, "permissions"))
}

// pidItems makes rows for bare PIDs, looked up as they scroll into view.
func pidItems(noids []string, note string) []listItem {
	items := make([]listItem, len(noids))
	for i, n := range noids {
		items[i] = listItem{pid: n, note: note, lookup: true}
	}
	return items
}

// when shows a timestamp readably, or as given if it isn't one.
func when(s string) string {
	if h, ok := humanTime(s); ok {
		return h
	}
	return s
}

// predicate turns "is_codebook_for" into "is codebook for".
func predicate(p string) string { return strings.ReplaceAll(p, "_", " ") }

// byNUID credits an edit: "by NUID 001234567 for 007654321".
func byNUID(actor, onBehalfOf string) string {
	if actor == "" {
		return ""
	}
	if onBehalfOf != "" {
		return "by NUID " + actor + " for " + onBehalfOf
	}
	return "by NUID " + actor
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func indentJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		return string(raw)
	}
	return buf.String()
}

// structFields turns a decoded struct back into ordered record fields, so a
// version descriptor renders with the same table as any record.
func structFields(v any) []atlas.Field {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	fields, err := atlas.OrderedFields(raw)
	if err != nil {
		return nil
	}
	return fields
}
