package shadow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompareIgnoresGeneratedValues(t *testing.T) {
	n := NewNormalizer("RUN123")

	tests := []struct {
		name      string
		reference string
		candidate string
		wantDiffs int
	}{
		{
			name:      "identical",
			reference: `{"path":"/a","size":1}`,
			candidate: `{"path":"/a","size":1}`,
		},
		{
			name:      "key order does not matter",
			reference: `{"path":"/a","size":1}`,
			candidate: `{"size":1,"path":"/a"}`,
		},
		{
			name:      "generated ids are canonicalised",
			reference: `{"id":"11111111-2222-3333-4444-555555555555"}`,
			candidate: `{"id":"99999999-8888-7777-6666-555555555555"}`,
		},
		{
			name:      "timestamps are canonicalised",
			reference: `{"date-created":1700000000000}`,
			candidate: `{"date-created":1799999999999}`,
		},
		{
			name:      "the run identifier is canonicalised",
			reference: `{"path":"/x/RUN123/a"}`,
			candidate: `{"path":"/x/RUN123/a"}`,
		},
		{
			name:      "which fixture copy was touched does not matter",
			reference: `{"path":"/x/A/a"}`,
			candidate: `{"path":"/x/B/a"}`,
		},
		{
			name:      "a real difference is reported",
			reference: `{"path":"/a","size":1}`,
			candidate: `{"path":"/a","size":2}`,
			wantDiffs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := n.Compare(
				Response{Status: 200, Body: []byte(tt.reference)},
				Response{Status: 200, Body: []byte(tt.candidate)},
			)
			if len(diffs) != tt.wantDiffs {
				t.Errorf("got %d diffs, want %d: %+v", len(diffs), tt.wantDiffs, diffs)
			}
		})
	}
}

// TestStatusIsComparedExactly is what preserving the error contract buys. There is no
// allowance for expected status differences, so every one is a real defect.
func TestStatusIsComparedExactly(t *testing.T) {
	n := NewNormalizer("RUN")

	diffs := n.Compare(
		Response{Status: 500, Body: []byte(`{"error_code":"ERR_DOES_NOT_EXIST"}`)},
		Response{Status: 404, Body: []byte(`{"error_code":"ERR_DOES_NOT_EXIST"}`)},
	)
	if len(diffs) != 1 || diffs[0].Kind != "status" {
		t.Errorf("a status difference was not reported: %+v", diffs)
	}
}

// TestListingOrderIsNotSorted guards the one thing the normaliser must not tidy. A paged
// listing's order is exactly what a sort-field request is asking for.
func TestListingOrderIsNotSorted(t *testing.T) {
	n := NewNormalizer("RUN")

	diffs := n.Compare(
		Response{Status: 200, Body: []byte(`{"files":["b","a"]}`)},
		Response{Status: 200, Body: []byte(`{"files":["a","b"]}`)},
	)
	if len(diffs) == 0 {
		t.Error("a reordered listing compared equal; sort order is the thing under test")
	}
}

// TestUnorderedArraysAreSorted covers the other side: an array whose order carries no
// meaning must not produce spurious differences.
func TestUnorderedArraysAreSorted(t *testing.T) {
	n := NewNormalizer("RUN")

	diffs := n.Compare(
		Response{Status: 200, Body: []byte(`{"groups":["b","a"]}`)},
		Response{Status: 200, Body: []byte(`{"groups":["a","b"]}`)},
	)
	if len(diffs) != 0 {
		t.Errorf("an unordered array produced differences: %+v", diffs)
	}
}

// TestCatalogLoads keeps the shipped catalog honest: a case file that no longer parses, or
// that repeats an id, would make a report ambiguous or silently drop cases.
func TestCatalogLoads(t *testing.T) {
	dir := filepath.Join("..", "..", "test", "shadow", "catalog")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no catalog directory: %v", err)
	}

	catalog, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	cases := catalog.Cases()
	if len(cases) == 0 {
		t.Fatal("the catalog is empty")
	}

	for _, c := range cases {
		if c.Method == "" || c.Path == "" {
			t.Errorf("case %q has no method or path", c.ID)
		}
		if c.Tier == "" {
			t.Errorf("case %q has no tier", c.ID)
		}
	}
	t.Logf("%d cases across %d groups", len(cases), len(catalog.Groups))
}

func TestCaseExpansion(t *testing.T) {
	c := Case{
		ID:    "x",
		Path:  "/users/{{.User}}/groups",
		Query: map[string]string{"user": "{{.User}}"},
		Body:  map[string]any{"paths": []any{"{{.Root}}/a"}},
	}

	expanded := c.Expand(map[string]string{"User": "wregglej", "Root": "/iplant/home/wregglej"})

	if expanded.Path != "/users/wregglej/groups" {
		t.Errorf("path = %q", expanded.Path)
	}
	if expanded.Query["user"] != "wregglej" {
		t.Errorf("query user = %q", expanded.Query["user"])
	}

	body := expanded.Body.(map[string]any)
	paths := body["paths"].([]any)
	if paths[0] != "/iplant/home/wregglej/a" {
		t.Errorf("body path = %v", paths[0])
	}

	// The original must not be mutated; a runner reuses cases across services.
	if c.Path != "/users/{{.User}}/groups" {
		t.Error("Expand mutated the original case")
	}
}
