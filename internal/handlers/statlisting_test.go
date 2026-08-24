package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/service"
)

// statListerResponse mirrors what /stat-lister returns, as a caller would read it.
type statListerResponse struct {
	Files   []service.Stat `json:"files"`
	Folders []service.Stat `json:"folders"`
	Total   int64          `json:"total"`
}

// TestStatListerSplitsAndPages covers the two things this endpoint exists to do that
// /stat-gatherer does not: split a page into files and folders, and report the total across
// the whole set rather than the page.
func TestStatListerSplitsAndPages(t *testing.T) {
	deps, fake := testDeps(t)
	deps.MaxPathsInRequest = 100
	fake.AddCollection(testHome+"/sub", icat.AccessOwn)
	fake.AddDataObject(testHome+"/b.txt", 200, icat.AccessRead)
	fake.SetUUID("id-a", testHome+"/a.txt")
	fake.SetUUID("id-b", testHome+"/b.txt")
	fake.SetUUID("id-sub", testHome+"/sub")

	stats := NewStats(deps)
	body := `{"ids":["id-a","id-b","id-sub"]}`

	tests := []struct {
		name        string
		query       string
		wantFiles   []string
		wantFolders []string
		wantTotal   int64
	}{
		{
			name:        "the whole set, folders and files apart",
			query:       "limit=50&offset=0",
			wantFiles:   []string{testHome + "/a.txt", testHome + "/b.txt"},
			wantFolders: []string{testHome + "/sub"},
			wantTotal:   3,
		},
		{
			name: "a page smaller than the set still reports the whole total",
			// Folders sort ahead of files, so the first page of one is the collection.
			query:       "limit=1&offset=0",
			wantFiles:   nil,
			wantFolders: []string{testHome + "/sub"},
			wantTotal:   3,
		},
		{
			name:        "an offset past the folders reaches the files",
			query:       "limit=1&offset=1",
			wantFiles:   []string{testHome + "/a.txt"},
			wantFolders: nil,
			wantTotal:   3,
		},
		{
			name:        "descending by name reverses within each type",
			query:       "limit=50&offset=0&sort-field=name&sort-dir=DESC",
			wantFiles:   []string{testHome + "/b.txt", testHome + "/a.txt"},
			wantFolders: []string{testHome + "/sub"},
			wantTotal:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, apierror.StyleOK, http.MethodPost,
				"/stat-lister?user="+testUser+"&"+tt.query, body, stats.Listing)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}

			var resp statListerResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
			}

			assertPaths(t, "files", resp.Files, tt.wantFiles)
			assertPaths(t, "folders", resp.Folders, tt.wantFolders)
			if resp.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", resp.Total, tt.wantTotal)
			}
		})
	}
}

// TestStatListerEmitsEmptyArrays matters because the client indexes into them: a null where
// an empty array belongs is a different shape, and the reference never emits one.
func TestStatListerEmitsEmptyArrays(t *testing.T) {
	deps, _ := testDeps(t)
	stats := NewStats(deps)

	rec := serve(t, apierror.StyleOK, http.MethodPost,
		"/stat-lister?user="+testUser+"&limit=50&offset=0", `{"ids":[]}`, stats.Listing)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	if got := rec.Body.String(); got != `{"files":[],"folders":[],"total":0}` {
		t.Errorf("body = %s", got)
	}
}

// TestStatListerRequiresItsParameters covers the ones the schema declares as required, which
// the reference rejects before the handler runs.
func TestStatListerRequiresItsParameters(t *testing.T) {
	deps, _ := testDeps(t)
	stats := NewStats(deps)

	tests := []struct {
		name  string
		query string
		body  string
	}{
		{"no limit", "user=" + testUser + "&offset=0", `{"ids":[]}`},
		{"no offset", "user=" + testUser + "&limit=50", `{"ids":[]}`},
		{"no user", "limit=50&offset=0", `{"ids":[]}`},
		{"no ids", "user=" + testUser + "&limit=50&offset=0", `{}`},
		{"negative offset", "user=" + testUser + "&limit=50&offset=-1", `{"ids":[]}`},
		{"zero limit", "user=" + testUser + "&limit=0&offset=0", `{"ids":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, apierror.StyleOK, http.MethodPost, "/stat-lister?"+tt.query, tt.body, stats.Listing)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// assertPaths compares the paths of a stat array against what the case expects.
func assertPaths(t *testing.T, label string, got []service.Stat, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s = %d entries, want %d (%v)", label, len(got), len(want), pathsOf(got))
	}
	for i := range want {
		if got[i].Path != want[i] {
			t.Errorf("%s[%d] = %q, want %q (full order %v)", label, i, got[i].Path, want[i], pathsOf(got))
		}
	}
}

func pathsOf(stats []service.Stat) []string {
	out := make([]string, 0, len(stats))
	for _, s := range stats {
		out = append(out, s.Path)
	}
	return out
}
