package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"hyperion/internal/atlas"
)

// section is one entry of an object's section menu; open builds its screen.
type section struct {
	label string
	open  func(*session, atlas.Object) screen
}

// sectionsFor lists the sections that apply to an object's type, in menu
// order: the full record, the type's own reads, then relationships and
// permissions. extra carries sections the caller already has data for (a
// FileSet opened from its Work knows its files; Atlas can't list them alone).
func sectionsFor(obj atlas.Object, extra []section) []section {
	ss := []section{{"Record", recordSection}}
	switch obj.Type {
	case atlas.TypeWork:
		ss = append(ss,
			section{"MODS", modsSection},
			section{"MODS history", modsHistorySection},
			section{"METS", metsSection},
			section{"FileSets", fileSetsSection},
			section{"Files", filesSection},
			section{"Linked collections", linkedSection},
			section{"Associations", associationsSection},
		)
	case atlas.TypeCollection, atlas.TypeCommunity:
		ss = append(ss,
			section{"Children", childrenSection},
			section{"MODS", modsSection},
			section{"MODS history", modsHistorySection},
		)
	case atlas.TypeSet:
		ss = append(ss, section{"Contents", contentsSection})
	case atlas.TypeFileSet:
		ss = append(ss, section{"METS", metsSection})
	case atlas.TypeFile:
		ss = append(ss,
			section{"FileSet and Work", fileParentsSection},
			section{"Versions", fileVersionsSection},
		)
	}
	ss = append(ss, extra...)
	if len(obj.Ancestors) > 0 {
		ss = append(ss, section{"Ancestors", ancestorsSection})
	}
	if len(referencedItems(obj)) > 0 {
		ss = append(ss, section{"Referenced PIDs", referencedSection})
	}
	if obj.Type != atlas.TypeSet { // a Compilation isn't a Resource; its ACL is in its record
		ss = append(ss, section{"Permissions", permissionsSection})
	}
	return ss
}

// objectContext is the dimmed line under a section's title naming its object.
func objectContext(obj atlas.Object) string {
	return fmt.Sprintf("%s %s · %s", obj.Type, obj.NOID, obj.Title)
}

func recordSection(sess *session, obj atlas.Object) screen {
	return staticPager(sess, "Record", objectContext(obj), fieldsRender(obj.Fields))
}

func modsSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "MODS", objectContext(obj), func(ctx context.Context, c *atlas.Client) (renderFunc, error) {
		xml, err := c.MODS(ctx, obj.NOID)
		if errors.Is(err, atlas.ErrNotFound) {
			return nil, userErr("This object has no MODS.")
		}
		return textRender(string(xml)), err
	})
}

func modsHistorySection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "MODS history", objectContext(obj), "No retained MODS versions.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			versions, err := c.MODSVersions(ctx, obj.NOID)
			if errors.Is(err, atlas.ErrForbidden) {
				return nil, userErr("MODS history is admin only: it names who made each edit.")
			}
			items := make([]listItem, len(versions))
			for i, v := range versions {
				versionID := v.VersionID
				items[i] = listItem{
					title: versionID,
					note:  joinNonEmpty(v.Created, byNUID(v.ActorNUID, v.OnBehalfOfNUID), v.Source, v.Note),
					open: func(sess *session) screen {
						return newPagerScreen(sess, "MODS as of "+versionID, objectContext(obj),
							func(ctx context.Context, c *atlas.Client) (renderFunc, error) {
								xml, err := c.MODSAt(ctx, obj.NOID, versionID)
								return textRender(string(xml)), err
							})
					},
				}
			}
			return items, err
		})
}

func metsSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "METS", objectContext(obj), func(ctx context.Context, c *atlas.Client) (renderFunc, error) {
		mets, err := c.METS(ctx, obj.Type, obj.NOID)
		if errors.Is(err, atlas.ErrNotFound) {
			return nil, userErr("No METS yet: Atlas builds it when the Work is completed.")
		}
		if err != nil {
			return nil, err
		}
		return textRender(indentJSON(mets)), nil
	})
}

func fileSetsSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "FileSets", objectContext(obj), "This Work has no FileSets.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			sets, err := c.FileSets(ctx, obj.NOID)
			return fileSetItems(sets), err
		})
}

// fileSetItems lists a Work's FileSets by position. Atlas has no route listing
// a FileSet's files, so each row hands the files this listing carries to the
// FileSet's own screen as an extra section.
func fileSetItems(sets []atlas.FileSetEntry) []listItem {
	items := make([]listItem, len(sets))
	for i, fs := range sets {
		title := "No position"
		if fs.Position != nil {
			title = fmt.Sprintf("Position %d", *fs.Position)
		}
		it := listItem{pid: fs.NOID, title: title, kind: "FileSet",
			note: joinNonEmpty(fs.Type, plural(len(fs.Assets), "file"))}
		if fs.Tombstoned {
			it.warn = "tombstoned"
		}
		it.open = func(sess *session) screen {
			return newObjectScreen(sess, fs.NOID, section{"Files", func(sess *session, fso atlas.Object) screen {
				return staticList(sess, "Files", objectContext(fso), "This FileSet has no files.", assetItems(fs.Assets))
			}})
		}
		items[i] = it
	}
	return items
}

func filesSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Files", objectContext(obj), "This Work has no files.",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			assets, err := c.Assets(ctx, obj.NOID)
			return assetItems(assets), err
		})
}

// assetItems lists a Work's assets: a File by filename, a Delegate by URI.
func assetItems(assets []atlas.Asset) []listItem {
	items := make([]listItem, len(assets))
	for i, a := range assets {
		kind, name := "File", firstNonEmpty(a.OriginalFilename, a.Filename)
		if a.Size == nil && a.URI != "" {
			kind, name = "Delegate", a.URI
		}
		size := ""
		if a.Size != nil {
			size = humanBytes(float64(*a.Size))
		}
		it := listItem{pid: a.NOID, title: name, kind: kind,
			note: joinNonEmpty(firstNonEmpty(a.Role, a.Use), a.MimeType, size)}
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
		})
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
		})
}

func childrenSection(sess *session, obj atlas.Object) screen {
	return newListScreen(sess, "Children", objectContext(obj), "Nothing is directly beneath this "+string(obj.Type)+".",
		func(ctx context.Context, c *atlas.Client) ([]listItem, error) {
			noids, err := c.Children(ctx, obj.Type, obj.NOID)
			return pidItems(noids, ""), err
		})
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
		})
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
		})
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
					note:  joinNonEmpty(v.Created, byNUID(v.ActorNUID, v.OnBehalfOfNUID), size),
					open: func(sess *session) screen {
						return staticPager(sess, title, objectContext(obj), fieldsRender(fields))
					},
				}
			}
			return items, err
		})
}

func ancestorsSection(sess *session, obj atlas.Object) screen {
	items := make([]listItem, len(obj.Ancestors))
	for i, a := range obj.Ancestors {
		items[i] = listItem{pid: a.NOID, title: a.Title, kind: a.Klass}
	}
	return staticList(sess, "Ancestors", objectContext(obj)+" · root first", "", items)
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
	return staticList(sess, "Referenced PIDs", objectContext(obj), "", referencedItems(obj))
}

func permissionsSection(sess *session, obj atlas.Object) screen {
	return newPagerScreen(sess, "Permissions", objectContext(obj), func(ctx context.Context, c *atlas.Client) (renderFunc, error) {
		fields, err := c.Permissions(ctx, obj.NOID)
		if err != nil {
			return nil, err
		}
		return fieldsRender(fields), nil
	})
}

// pidItems makes rows for bare PIDs, looked up as they scroll into view.
func pidItems(noids []string, note string) []listItem {
	items := make([]listItem, len(noids))
	for i, n := range noids {
		items[i] = listItem{pid: n, note: note, lookup: true}
	}
	return items
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
