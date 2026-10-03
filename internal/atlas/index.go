package atlas

import (
	"context"
	"encoding/json"
	"net/url"
)

// The two routes for debugging search: an object's Solr document, and Solr's
// account of how the catalog search scores the object for some words. Both
// are for admins and delegated admins who can read the object.

func PathSolr(id string) string { return "/resources/" + esc(id) + "/solr" }

// PathSearchExplanation builds the explanation route. q is user input, so it
// goes through url.Values.
func PathSearchExplanation(id, q string) string {
	return "/resources/" + esc(id) + "/search_explanation?" + url.Values{"q": {q}}.Encode()
}

// IndexedTypes are the types Atlas writes to Solr. A Set is not one of them.
var IndexedTypes = []Type{TypeWork, TypeCollection, TypeCommunity, TypeFileSet, TypeFile, TypeDelegate, TypePerson}

// ExplainedTypes are the types the search explanation covers: the ones a
// librarian searches for.
var ExplainedTypes = []Type{TypeWork, TypeCollection, TypeCommunity}

// SolrDocument reads an object's Solr document (GET /resources/:id/solr) as
// Solr stores it, its fields in Solr's order.
func (c *Client) SolrDocument(ctx context.Context, noid string) ([]Field, error) {
	path := PathSolr(noid)
	var wrap struct {
		Document json.RawMessage `json:"document"`
	}
	if err := c.getJSON(ctx, path, &wrap); err != nil {
		return nil, err
	}
	fields, err := OrderedFields(wrap.Document)
	if err != nil {
		return nil, badResponse(path, err)
	}
	return fields, nil
}

// SearchExplanation is Atlas's answer to "how does the catalog search score
// this object for q?": Solr's own pieces, grouped but not interpreted.
type SearchExplanation struct {
	NOID        string           `json:"noid"`
	Q           string           `json:"q"`
	Matched     bool             `json:"matched"`
	Score       float64          `json:"score"`
	HiddenBy    []string         `json:"hidden_by"`
	ParsedQuery string           `json:"parsed_query"`
	Handler     SearchHandler    `json:"handler"`
	Explanation ExplainNode      `json:"explanation"`
	Fields      []ExplainedField `json:"fields"`
}

// SearchHandler is the search handler's settings, read from Solr. QF and PF
// map a field to its boost; their order is lost in JSON, so Fields keeps qf's.
type SearchHandler struct {
	QF    map[string]float64 `json:"qf"`
	PF    map[string]float64 `json:"pf"`
	MM    *string            `json:"mm"`
	Tie   *float64           `json:"tie"`
	Boost *string            `json:"boost"`
}

// ExplainNode is one node of Solr's structured explanation tree.
type ExplainNode struct {
	Match       bool          `json:"match"`
	Value       float64       `json:"value"`
	Description string        `json:"description"`
	Details     []ExplainNode `json:"details"`
}

// ExplainedField is one qf field: the stored text that feeds it, analysed
// against the words. Reason says why a field has no text: not_stored,
// full_text, analysis_failed or not_in_schema ("" otherwise).
type ExplainedField struct {
	Field       string       `json:"field"`
	Stored      bool         `json:"stored"`
	QueryTokens *QueryTokens `json:"query_tokens"`
	Values      []FieldValue `json:"values"`
	Truncated   bool         `json:"truncated"`
	Reason      string       `json:"reason"`
	Highlights  []string     `json:"highlights"`
}

// QueryTokens are the words as the field's analyzer first splits them, and as
// Solr finally searches for them.
type QueryTokens struct {
	Typed    []string `json:"typed"`
	Analysed []string `json:"analysed"`
}

// FieldValue is one stored value, from Source, and its index tokens.
type FieldValue struct {
	Source string  `json:"source"`
	Text   string  `json:"text"`
	Tokens []Token `json:"tokens"`
}

// Token is one index token. Start and End count UTF-16 code units into the
// value's text, as Solr reports them.
type Token struct {
	Text     string `json:"text"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Position int    `json:"position"`
	Match    bool   `json:"match"`
}

// ExplainSearch asks how the catalog search scores noid for q
// (GET /resources/:id/search_explanation). q must not be blank.
func (c *Client) ExplainSearch(ctx context.Context, noid, q string) (SearchExplanation, error) {
	var res SearchExplanation
	err := c.getJSON(ctx, PathSearchExplanation(noid, q), &res)
	return res, err
}
