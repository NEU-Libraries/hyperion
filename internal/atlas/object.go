package atlas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Type is the kind of an Atlas object, as hyperion names it to users.
type Type string

const (
	TypeWork       Type = "Work"
	TypeCollection Type = "Collection"
	TypeCommunity  Type = "Community"
	TypeFileSet    Type = "FileSet"
	TypeFile       Type = "File" // Atlas's Blob, served at /files/:id
	TypeDelegate   Type = "Delegate"
	TypePerson     Type = "Person"
	TypeSet        Type = "Set" // Atlas's Compilation
)

// typeByRoute maps the first segment of a typed route to its Type.
var typeByRoute = map[string]Type{
	"works":        TypeWork,
	"collections":  TypeCollection,
	"communities":  TypeCommunity,
	"file_sets":    TypeFileSet,
	"files":        TypeFile,
	"delegates":    TypeDelegate,
	"people":       TypePerson,
	"compilations": TypeSet,
}

// Ancestor is one step of an object's ancestor chain (root-first).
type Ancestor struct {
	NOID  string `json:"noid"`
	Klass string `json:"klass"`
	Title string `json:"title"`
}

// Field is one key of an object's record, in the order Atlas sent it. Value is
// the raw JSON so the caller decides how to render strings, nulls, and nesting.
type Field struct {
	Key   string
	Value json.RawMessage
}

// Object is any Atlas resource resolved from a bare NOID: its type, the typed
// route it was read from, and its record both parsed and raw.
type Object struct {
	Type      Type
	NOID      string
	Path      string
	Title     string
	Ancestors []Ancestor
	Fields    []Field
	Raw       []byte
}

// Inspect resolves a NOID of any type and reads its record. GET /resources/:id
// answers 302 to the typed route for every Valkyrie-backed type; a Compilation
// is not one of those and 404s there, so a 404 falls back to /compilations/:id.
func (c *Client) Inspect(ctx context.Context, noid string) (Object, error) {
	id := url.PathEscape(strings.TrimSpace(noid))
	path, err := c.locate(ctx, id)
	if errors.Is(err, ErrNotFound) {
		path = "/compilations/" + id
	} else if err != nil {
		return Object{}, err
	}

	// A tombstoned resource answers 410 Gone with its full record — still worth
	// inspecting, so 410 reads like 200 here (the record says tombstoned: true).
	resp, body, err := c.send(ctx, c.http, path)
	if err != nil {
		return Object{}, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusGone {
		return Object{}, statusError(path, resp.StatusCode, body)
	}
	obj, err := parseObject(body)
	if err != nil {
		return Object{}, badResponse(path, err)
	}
	obj.Type = typeByRoute[strings.Split(strings.TrimPrefix(path, "/"), "/")[0]]
	obj.Path = path
	return obj, nil
}

// locate asks /resources/:id where a NOID lives and returns the typed path
// (e.g. /works/abc123). It reads the redirect rather than following it: only
// the last two segments of Location are used, joined to the client's own base
// URL, so a redirect built with the wrong scheme or host behind a proxy still
// resolves — and the token is never sent anywhere but the configured Atlas.
func (c *Client) locate(ctx context.Context, id string) (string, error) {
	path := "/resources/" + id
	resp, body, err := c.send(ctx, c.noFollow, path)
	if err != nil {
		return "", err
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		if resp.StatusCode == http.StatusOK {
			return "", badResponse(path, errors.New("answered 200, expected a redirect"))
		}
		return "", statusError(path, resp.StatusCode, body)
	}

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return "", badResponse(path, fmt.Errorf("redirected to an unreadable Location: %w", err))
	}
	segs := strings.Split(strings.Trim(loc.EscapedPath(), "/"), "/")
	if len(segs) < 2 {
		return "", badResponse(path, fmt.Errorf("redirected to unexpected %q", loc))
	}
	route, target := segs[len(segs)-2], segs[len(segs)-1]
	if _, ok := typeByRoute[route]; !ok {
		return "", badResponse(path, fmt.Errorf("redirected to unknown type %q", route))
	}
	return "/" + route + "/" + target, nil
}

// parseObject reads a typed show payload — one wrapper key ({"work": {...}},
// {"blob": {...}}, …) around the record — keeping the record's key order.
func parseObject(body []byte) (Object, error) {
	var wrap map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrap); err != nil {
		return Object{}, err
	}
	if len(wrap) != 1 {
		return Object{}, fmt.Errorf("expected one wrapper key, got %d", len(wrap))
	}
	var record json.RawMessage
	for _, v := range wrap {
		record = v
	}

	fields, err := OrderedFields(record)
	if err != nil {
		return Object{}, err
	}
	obj := Object{Fields: fields, Raw: body}
	for _, f := range fields {
		switch f.Key {
		case "id":
			_ = json.Unmarshal(f.Value, &obj.NOID)
		case "ancestors":
			_ = json.Unmarshal(f.Value, &obj.Ancestors)
		}
	}
	obj.Title = objectTitle(fields)
	return obj, nil
}

// OrderedFields splits a JSON object into its keys in document order, which a
// Go map would lose.
func OrderedFields(record json.RawMessage) ([]Field, error) {
	dec := json.NewDecoder(bytes.NewReader(record))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("record is not an object")
	}
	var fields []Field
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		fields = append(fields, Field{Key: tok.(string), Value: v})
	}
	return fields, nil
}

// objectTitle picks the human name each type carries: a title for the
// descriptive types, a display name for a Person, a filename for a File or
// Delegate.
func objectTitle(fields []Field) string {
	byKey := make(map[string]json.RawMessage, len(fields))
	for _, f := range fields {
		byKey[f.Key] = f.Value
	}
	for _, k := range []string{"title", "display_name", "original_filename", "filename", "label"} {
		var s string
		if json.Unmarshal(byKey[k], &s) == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return title("")
}
