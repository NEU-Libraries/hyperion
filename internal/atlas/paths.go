package atlas

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Paths of the Atlas routes hyperion reads, with the id path-escaped. The
// client's own reads use them, and so does anything showing a route's raw
// response, so the two can't drift apart. A route that doesn't exist for a
// type answers "".

func esc(id string) string { return url.PathEscape(id) }

func PathMODS(id string) string             { return "/resources/" + esc(id) + "/mods.xml" }
func PathMODSJSON(id string) string         { return "/resources/" + esc(id) + "/mods" }
func PathMODSVersions(id string) string     { return "/resources/" + esc(id) + "/mods/versions" }
func PathMODSVersion(id, v string) string   { return PathMODSVersions(id) + "/" + esc(v) }
func PathPermissions(id string) string      { return "/resources/" + esc(id) + "/permissions" }
func PathResourceChildren(id string) string { return "/resources/" + esc(id) + "/children" }
func PathFileSets(id string) string         { return "/works/" + esc(id) + "/file_sets" }
func PathAssets(id string) string           { return "/works/" + esc(id) + "/assets" }
func PathLinkedMembers(id string) string    { return "/works/" + esc(id) + "/linked_members" }
func PathAssociations(id string) string     { return "/works/" + esc(id) + "/associations" }
func PathFileAncestry(id string) string     { return "/files/" + esc(id) + "/ancestry" }
func PathFileVersions(id string) string     { return "/files/" + esc(id) + "/versions" }
func PathSetContents(id string, page int) string {
	return fmt.Sprintf("/compilations/%s/contents?page=%d&per_page=100", esc(id), page)
}

// PathSearch builds the catalog search route. The text is user input, so it
// goes through url.Values rather than string joining; an empty q or t is left
// out.
func PathSearch(q string, t Type, page, perPage int) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if t != "" {
		v.Set("type", string(t))
	}
	v.Set("page", strconv.Itoa(page))
	v.Set("per_page", strconv.Itoa(perPage))
	return "/resources/search?" + v.Encode()
}

func PathChildren(t Type, id string) string {
	route := map[Type]string{TypeCollection: "collections", TypeCommunity: "communities"}[t]
	if route == "" {
		return ""
	}
	return "/" + route + "/" + esc(id) + "/children"
}

func PathMETS(t Type, id string) string {
	route := map[Type]string{TypeWork: "works", TypeFileSet: "file_sets"}[t]
	if route == "" {
		return ""
	}
	return "/" + route + "/" + esc(id) + "/mets"
}

// Raw reads any route and returns the body exactly as Atlas sent it. A 410
// counts as an answer, since a tombstoned resource's body is its record.
func (c *Client) Raw(ctx context.Context, path string) ([]byte, error) {
	resp, body, err := c.send(ctx, c.http, path)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusGone {
		return nil, statusError(path, resp.StatusCode, body)
	}
	return body, nil
}
