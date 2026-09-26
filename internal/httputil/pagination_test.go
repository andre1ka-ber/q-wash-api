package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParsePagination(t *testing.T) {
	cases := []struct {
		name, query      string
		wantPage, wantSz int
	}{
		{"defaults", "", 1, DefaultPageSize},
		{"explicit", "page=3&page_size=10", 3, 10},
		{"page_size is capped", "page_size=100000", 1, MaxPageSize},
		{"zero and negative fall back", "page=0&page_size=-5", 1, DefaultPageSize},
		{"garbage falls back", "page=abc&page_size=", 1, DefaultPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x?"+tc.query, nil)
			p := ParsePagination(r)
			if p.Page != tc.wantPage || p.PageSize != tc.wantSz {
				t.Fatalf("got page=%d size=%d, want page=%d size=%d", p.Page, p.PageSize, tc.wantPage, tc.wantSz)
			}
		})
	}
}

func TestPaginationOffsetLimit(t *testing.T) {
	p := Pagination{Page: 3, PageSize: 20}
	if p.Offset() != 40 || p.Limit() != 20 {
		t.Fatalf("offset=%d limit=%d, want 40/20", p.Offset(), p.Limit())
	}
}

func TestWritePaginatedEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	WritePaginated(rec, http.StatusOK, []string{"a", "b"}, Pagination{Page: 2, PageSize: 2}, 5)

	var body struct {
		Items    []string `json:"items"`
		Page     int      `json:"page"`
		PageSize int      `json:"page_size"`
		Total    int64    `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(body.Items) != 2 || body.Page != 2 || body.PageSize != 2 || body.Total != 5 {
		t.Fatalf("unexpected envelope: code=%d %+v", rec.Code, body)
	}
}
