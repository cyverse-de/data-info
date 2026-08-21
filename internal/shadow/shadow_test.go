package shadow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestSchemaReasonWithEscapedQuotes guards a harness bug. These reasons quote the value that
// failed, so they routinely contain escaped quotes; a pattern that stopped at the first one
// left a remainder that no longer parsed, and the case reported "not JSON" rather than
// comparing.
func TestSchemaReasonWithEscapedQuotes(t *testing.T) {
	n := NewNormalizer("RUN")

	body := []byte(`{"error_code":"ERR_ILLEGAL_ARGUMENT","reason":"(not (map? \"not an object\"))"}`)
	other := []byte(`{"error_code":"ERR_ILLEGAL_ARGUMENT","reason":"something else entirely"}`)

	diffs := n.Compare(Response{Status: 400, Body: body}, Response{Status: 400, Body: other})
	for _, d := range diffs {
		if strings.Contains(d.Detail, "not JSON") {
			t.Fatalf("a reason containing escaped quotes broke parsing: %s", d.Detail)
		}
	}
	if len(diffs) != 0 {
		t.Errorf("the reason should be normalised away, leaving no difference: %+v", diffs)
	}
}

// TestScratchGuardRejectsDangerousRoots covers the check that stands between a mistyped flag
// and other people's data. The harness creates and deletes in a shared zone, so a root it
// should not accept must be refused before anything is created, not after.
func TestScratchGuardRejectsDangerousRoots(t *testing.T) {
	tests := []struct {
		name string
		root string
	}{
		{"empty", ""},
		{"relative", "cyverse/home/x/shadow"},
		{"the zone root", "/cyverse"},
		{"the home collection", "/cyverse/home"},
		{"someone's home", "/cyverse/home/wregglej"},
		{"deep but not named as scratch", "/cyverse/home/wregglej/important-data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewScratchGuard(tt.root); err == nil {
				t.Errorf("NewScratchGuard accepted %q", tt.root)
			}
		})
	}
}

func TestScratchGuardAcceptsAScratchRoot(t *testing.T) {
	guard, err := NewScratchGuard("/cyverse/home/de-irods/shadow-scratch")
	if err != nil {
		t.Fatalf("NewScratchGuard: %v", err)
	}
	if guard.Root != "/cyverse/home/de-irods/shadow-scratch" {
		t.Errorf("root = %q", guard.Root)
	}
}

// TestScratchGuardConfinesPaths is the second check: every path derived from the root is
// tested again before it is used, so a bug in path construction cannot reach outside.
func TestScratchGuardConfinesPaths(t *testing.T) {
	guard, err := NewScratchGuard("/cyverse/home/de-irods/shadow-scratch")
	if err != nil {
		t.Fatalf("NewScratchGuard: %v", err)
	}

	allowed := []string{
		"/cyverse/home/de-irods/shadow-scratch",
		"/cyverse/home/de-irods/shadow-scratch/run/A/case",
		"/cyverse/home/de-irods/shadow-scratch/",
	}
	for _, path := range allowed {
		if err := guard.Check(path); err != nil {
			t.Errorf("Check(%q) refused a path inside the root: %v", path, err)
		}
	}

	refused := []string{
		"/cyverse/home/de-irods",
		"/cyverse/home/someone-else",
		"/cyverse/home/de-irods/shadow-scratch-other",
		"/cyverse/home/de-irods/shadow-scratch/../../elsewhere",
		"/",
	}
	for _, path := range refused {
		if err := guard.Check(path); err == nil {
			t.Errorf("Check(%q) allowed a path outside the root", path)
		}
	}
}

// TestSidePathsDiffer is what keeps the two services from contending: each gets its own copy
// of a case's fixture.
func TestSidePathsDiffer(t *testing.T) {
	guard, err := NewScratchGuard("/cyverse/home/de-irods/shadow-scratch")
	if err != nil {
		t.Fatalf("NewScratchGuard: %v", err)
	}

	reference := guard.SidePath("run1", "case1", SideReference)
	candidate := guard.SidePath("run1", "case1", SideCandidate)

	if reference == candidate {
		t.Fatal("both services were given the same fixture path")
	}
	for _, path := range []string{reference, candidate} {
		if err := guard.Check(path); err != nil {
			t.Errorf("a generated fixture path is outside the root: %v", err)
		}
	}

	// The side is canonicalised out of responses, so the two compare equal.
	n := NewNormalizer("run1")
	diffs := n.Compare(
		Response{Status: 200, Body: []byte(`{"path":"` + reference + `"}`)},
		Response{Status: 200, Body: []byte(`{"path":"` + candidate + `"}`)},
	)
	if len(diffs) != 0 {
		t.Errorf("the two sides did not compare equal after canonicalisation: %+v", diffs)
	}
}

// TestWriteCasesAreSkippedWithoutScratch keeps the harness honest: without a scratch
// collection it must refuse write cases and say so, not run them against a shared tree.
func TestWriteCasesAreSkippedWithoutScratch(t *testing.T) {
	runner := NewRunner("http://reference", "http://candidate", "run")

	result := runner.runOne(context.Background(), Case{ID: "x", Tier: TierWrite, Method: "POST", Path: "/data/directories"})
	if result.Skipped == "" {
		t.Error("a write case ran without a scratch collection")
	}
}
