package atlas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// The reads behind the inspector's sections: an object's relationships,
// descriptive and structural metadata, files, versions, and permissions. Each
// takes a bare NOID; ids are path-escaped here so a caller can pass user input.

// Digest is the lightweight {noid, klass, title} shape Atlas uses for resolved
// Set contents and descendant works.
type Digest struct {
	NOID  string `json:"noid"`
	Klass string `json:"klass"`
	Title string `json:"title"`
}

// Asset is one downloadable thing on a Work: a held binary (a File — size and
// filename set) or a pointer-only Delegate (uri and use set).
type Asset struct {
	NOID             string `json:"noid"`
	MimeType         string `json:"mime_type"`
	OriginalFilename string `json:"original_filename"`
	Filename         string `json:"filename"`
	Size             *int64 `json:"size"`
	Use              string `json:"use"`
	URI              string `json:"uri"`
	Label            string `json:"label"`
	Role             string `json:"role"`
	Classification   string `json:"classification"`
	Gated            bool   `json:"gated"`
}

// FileSetEntry is one page-bearing FileSet of a Work, with its assets.
type FileSetEntry struct {
	NOID       string  `json:"noid"`
	Type       string  `json:"type"`
	Position   *int    `json:"position"`
	Tombstoned bool    `json:"tombstoned"`
	Assets     []Asset `json:"assets"`
}

// Associations are a Work's typed links to other Works, keyed by predicate
// (is_codebook_for, …): what it asserts (Outbound) and what others assert about
// it (Inbound).
type Associations struct {
	Outbound map[string][]string `json:"outbound"`
	Inbound  map[string][]string `json:"inbound"`
}

// FileAncestry is a File's containing FileSet and Work; either may be empty.
type FileAncestry struct {
	FileSet string `json:"file_set"`
	Work    string `json:"work"`
}

// FileVersion is one retained content revision of a File, newest first.
type FileVersion struct {
	Revision         int    `json:"revision"`
	VersionID        string `json:"version_id"`
	FileIdentifier   string `json:"file_identifier"`
	Created          string `json:"created"`
	ActorNUID        string `json:"actor_nuid"`
	OnBehalfOfNUID   string `json:"on_behalf_of_nuid"`
	Digest           string `json:"digest"`
	Size             *int64 `json:"size"`
	OriginalFilename string `json:"original_filename"`
}

// MODSVersion describes one retained MODS version, newest first.
type MODSVersion struct {
	VersionID      string `json:"version_id"`
	Created        string `json:"created"`
	ActorNUID      string `json:"actor_nuid"`
	OnBehalfOfNUID string `json:"on_behalf_of_nuid"`
	Source         string `json:"source"`
	Note           string `json:"note"`
}

// Children lists the NOIDs directly beneath a Collection or Community.
func (c *Client) Children(ctx context.Context, t Type, id string) ([]string, error) {
	route := map[Type]string{TypeCollection: "collections", TypeCommunity: "communities"}[t]
	if route == "" {
		return nil, fmt.Errorf("atlas: a %s has no children route", t)
	}
	var noids []string
	err := c.getJSON(ctx, "/"+route+"/"+url.PathEscape(id)+"/children", &noids)
	return noids, err
}

// SetContents walks every page of a Set's resolved contents.
func (c *Client) SetContents(ctx context.Context, id string) ([]Digest, error) {
	var all []Digest
	for page := 1; ; page++ {
		var res struct {
			Contents   []Digest `json:"contents"`
			Pagination struct {
				Pages int `json:"pages"`
			} `json:"pagination"`
		}
		path := fmt.Sprintf("/compilations/%s/contents?page=%d&per_page=100", url.PathEscape(id), page)
		if err := c.getJSON(ctx, path, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Contents...)
		if len(res.Contents) == 0 || page >= res.Pagination.Pages {
			return all, nil
		}
	}
}

// FileSets lists a Work's page FileSets in order, each with its assets.
func (c *Client) FileSets(ctx context.Context, id string) ([]FileSetEntry, error) {
	var out []FileSetEntry
	err := c.getJSON(ctx, "/works/"+url.PathEscape(id)+"/file_sets", &out)
	return out, err
}

// Assets lists every downloadable asset of a Work, flattened across FileSets.
func (c *Client) Assets(ctx context.Context, id string) ([]Asset, error) {
	var out []Asset
	err := c.getJSON(ctx, "/works/"+url.PathEscape(id)+"/assets", &out)
	return out, err
}

// LinkedCollections lists the Collections a Work is linked into beyond its
// structural parent.
func (c *Client) LinkedCollections(ctx context.Context, id string) ([]string, error) {
	var out []string
	err := c.getJSON(ctx, "/works/"+url.PathEscape(id)+"/linked_members", &out)
	return out, err
}

// WorkAssociations reads a Work's associations in both directions.
func (c *Client) WorkAssociations(ctx context.Context, id string) (Associations, error) {
	var out Associations
	err := c.getJSON(ctx, "/works/"+url.PathEscape(id)+"/associations", &out)
	return out, err
}

// FileAncestry resolves a File to its FileSet and Work.
func (c *Client) FileAncestry(ctx context.Context, id string) (FileAncestry, error) {
	var out struct {
		FileSet *string `json:"file_set"`
		Work    *string `json:"work"`
	}
	if err := c.getJSON(ctx, "/files/"+url.PathEscape(id)+"/ancestry", &out); err != nil {
		return FileAncestry{}, err
	}
	var a FileAncestry
	if out.FileSet != nil {
		a.FileSet = *out.FileSet
	}
	if out.Work != nil {
		a.Work = *out.Work
	}
	return a, nil
}

// FileVersions lists a File's retained content revisions, newest first.
func (c *Client) FileVersions(ctx context.Context, id string) ([]FileVersion, error) {
	var out struct {
		Versions []FileVersion `json:"versions"`
	}
	err := c.getJSON(ctx, "/files/"+url.PathEscape(id)+"/versions", &out)
	return out.Versions, err
}

// MODSVersions lists a resource's retained MODS versions, newest first. Atlas
// gates this on admin, since the descriptors carry edit attribution; anyone
// else gets ErrForbidden.
func (c *Client) MODSVersions(ctx context.Context, id string) ([]MODSVersion, error) {
	var out struct {
		Versions []MODSVersion `json:"versions"`
	}
	err := c.getJSON(ctx, "/resources/"+url.PathEscape(id)+"/mods/versions", &out)
	return out.Versions, err
}

// MODSAt fetches the raw MODS XML as of one retained version.
func (c *Client) MODSAt(ctx context.Context, id, versionID string) ([]byte, error) {
	return c.get(ctx, "/resources/"+url.PathEscape(id)+"/mods/versions/"+url.PathEscape(versionID))
}

// METS reads the JSON projection of a Work's or FileSet's structural metadata.
// A Work that has never been completed has none, and answers ErrNotFound.
func (c *Client) METS(ctx context.Context, t Type, id string) (json.RawMessage, error) {
	route := map[Type]string{TypeWork: "works", TypeFileSet: "file_sets"}[t]
	if route == "" {
		return nil, fmt.Errorf("atlas: a %s has no METS", t)
	}
	var wrap map[string]struct {
		METS json.RawMessage `json:"mets"`
	}
	if err := c.getJSON(ctx, "/"+route+"/"+url.PathEscape(id)+"/mets", &wrap); err != nil {
		return nil, err
	}
	for _, v := range wrap {
		return v.METS, nil
	}
	return nil, fmt.Errorf("atlas: empty METS response for %s", id)
}

// Permissions reads a resource's ACL envelope as ordered fields.
func (c *Client) Permissions(ctx context.Context, id string) ([]Field, error) {
	path := "/resources/" + url.PathEscape(id) + "/permissions"
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	obj, err := parseObject(body)
	if err != nil {
		return nil, fmt.Errorf("atlas: unexpected response for %s: %w", path, err)
	}
	return obj.Fields, nil
}
