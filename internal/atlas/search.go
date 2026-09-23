package atlas

import "context"

// SearchPageSize is how many hits one Search reads: Atlas's cap on per_page.
const SearchPageSize = 100

// SearchTypes are the types the catalog search covers, and the values its
// type filter takes. FileSets, Files, Delegates and Sets aren't indexed.
var SearchTypes = []Type{TypeWork, TypeCollection, TypeCommunity, TypePerson}

// SearchHit is one catalog search result: a digest, plus what a result row
// shows. Year is nil when the object has no date.
type SearchHit struct {
	Digest
	Creators   []string `json:"creators"`
	Year       *string  `json:"year"`
	InProgress bool     `json:"in_progress"`
	Embargoed  bool     `json:"embargoed"`
	Incomplete bool     `json:"incomplete"`
}

// SearchPage is one page of catalog search results, in Atlas's fixed
// relevance order, already gated for the token's user.
type SearchPage struct {
	Results    []SearchHit `json:"results"`
	Pagination struct {
		Total   int `json:"total"`
		Page    int `json:"page"`
		PerPage int `json:"per_page"`
		Pages   int `json:"pages"`
	} `json:"pagination"`
}

// Search runs a keyword search over the catalog (GET /resources/search) and
// reads one page of SearchPageSize hits. t narrows it to one of SearchTypes;
// "" searches them all.
func (c *Client) Search(ctx context.Context, q string, t Type, page int) (SearchPage, error) {
	var res SearchPage
	err := c.getJSON(ctx, PathSearch(q, t, page, SearchPageSize), &res)
	return res, err
}
