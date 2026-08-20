package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// serveRoute runs a request through a router with a real route pattern, which the wildcard
// handlers need in order to see their parameters.
func serveRoute(t *testing.T, style apierror.Style, method, pattern, target string, handler echo.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = apierror.HTTPErrorHandler(nil)

	var middleware []echo.MiddlewareFunc
	if style != apierror.StyleTrap {
		middleware = append(middleware, apierror.WithStyle(style))
	}
	e.Add(method, pattern, handler, middleware...)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestFolderListing(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddDataObject(testHome+"/b.txt", 200, icat.AccessRead)
	fake.AddCollection(testHome+"/sub", icat.AccessOwn)

	listings := NewListings(deps)
	rec := serveRoute(t, apierror.StyleOK, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser, listings.FolderListing)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var listing service.Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if len(listing.Folders) != 1 || listing.Folders[0].Name != "sub" {
		t.Errorf("folders = %+v, want one named sub", listing.Folders)
	}
	if len(listing.Files) != 2 {
		t.Errorf("files = %d, want 2", len(listing.Files))
	}
	if listing.Total == nil || *listing.Total != 3 {
		t.Errorf("total = %v, want 3", listing.Total)
	}
	// The folder describes itself at the top level rather than in a wrapper.
	if listing.Path != testHome {
		t.Errorf("the listing's own path = %q, want %q", listing.Path, testHome)
	}
	if listing.Readme != false {
		t.Errorf("readme = %v, want false", listing.Readme)
	}
}

// TestListingSortOrderIsPassedThrough matters because the order is the contract: a
// sort-field request is asking for exactly it.
func TestListingSortOrderIsPassedThrough(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddDataObject(testHome+"/b.txt", 200, icat.AccessRead)
	fake.AddDataObject(testHome+"/c.txt", 300, icat.AccessRead)
	listings := NewListings(deps)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"ascending by name", "sort-field=name&sort-dir=ASC", []string{"a.txt", "b.txt", "c.txt"}},
		{"descending by name", "sort-field=name&sort-dir=DESC", []string{"c.txt", "b.txt", "a.txt"}},
		{"ascending by size", "sort-field=size&sort-dir=ASC", []string{"a.txt", "b.txt", "c.txt"}},
		{"descending by size", "sort-field=size&sort-dir=DESC", []string{"c.txt", "b.txt", "a.txt"}},
		{"lowercase direction is accepted", "sort-field=name&sort-dir=desc", []string{"c.txt", "b.txt", "a.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveRoute(t, apierror.StyleOK, http.MethodGet, "/data/path/:zone/*",
				"/data/path/iplant/home/wregglej?user="+testUser+"&"+tt.query, listings.FolderListing)

			var listing service.Listing
			if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
				t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
			}

			got := make([]string, 0, len(listing.Files))
			for _, f := range listing.Files {
				got = append(got, f.Name)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("position %d = %q, want %q (full order %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// TestListingRejectsUnsortableField is a deliberate improvement over the reference, which
// lets the value reach a bare exception and answers 500 with no code.
func TestListingRejectsUnsortableField(t *testing.T) {
	deps, _ := testDeps(t)
	listings := NewListings(deps)

	rec := serveRoute(t, apierror.StyleOK, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser+"&sort-field=nonsense", listings.FolderListing)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestPathsWithAwkwardCharacters covers names iRODS allows and a URL router would otherwise
// mangle. An encoded slash in particular must stay inside the name.
func TestPathsWithAwkwardCharacters(t *testing.T) {
	deps, fake := testDeps(t)
	listings := NewListings(deps)

	tests := []struct {
		name     string
		target   string
		wantPath string
	}{
		{"a space", "/data/path/iplant/home/wregglej/a%20b", testHome + "/a b"},
		{"a hash", "/data/path/iplant/home/wregglej/c%234", testHome + "/c#4"},
		{"a percent", "/data/path/iplant/home/wregglej/100%25", testHome + "/100%"},
		{"an encoded slash stays in the name", "/data/path/iplant/home/wregglej/a%2Fb", testHome + "/a/b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake.AddCollection(tt.wantPath, icat.AccessOwn)

			rec := serveRoute(t, apierror.StyleOK, http.MethodGet, "/data/path/:zone/*",
				tt.target+"?user="+testUser, listings.FolderListing)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (%s); the path was mangled", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNavigationListsOnlyFolders(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddCollection(testHome+"/sub", icat.AccessOwn)
	listings := NewListings(deps)

	rec := serveRoute(t, apierror.StyleOK, http.MethodGet, "/navigation/path/:zone/*",
		"/navigation/path/iplant/home/wregglej?user="+testUser, listings.Navigation)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Folder struct {
			Path    string                `json:"path"`
			Folders []service.FolderEntry `json:"folders"`
		} `json:"folder"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if resp.Folder.Path != testHome {
		t.Errorf("path = %q, want %q", resp.Folder.Path, testHome)
	}
	// a.txt is a file and must not appear: this endpoint backs a tree view.
	if len(resp.Folder.Folders) != 1 || resp.Folder.Folders[0].Path != testHome+"/sub" {
		t.Errorf("folders = %+v, want only the subfolder", resp.Folder.Folders)
	}
}

func TestHeadStatuses(t *testing.T) {
	deps, fake := testDeps(t)
	fake.SetUUID("11111111-2222-3333-4444-555555555555", testHome+"/a.txt")
	listings := NewListings(deps)

	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"readable", "/data/11111111-2222-3333-4444-555555555555?user=" + testUser, http.StatusOK},
		{"unknown id", "/data/99999999-8888-7777-6666-555555555555?user=" + testUser, http.StatusNotFound},
		// A 400, not a 422: the path parameter is typed as a UUID, so an unparseable one
		// fails schema coercion before the handler runs. Verified against the reference.
		{"not a uuid", "/data/not-a-uuid?user=" + testUser, http.StatusBadRequest},
		{"unknown user", "/data/11111111-2222-3333-4444-555555555555?user=nobody", http.StatusUnprocessableEntity},
		{"no user", "/data/11111111-2222-3333-4444-555555555555", http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveRoute(t, apierror.StyleTrap, http.MethodHead, "/data/:data-id", tt.target, listings.Head)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			// The contract is the status; a HEAD response carries no body.
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body.String())
			}
		})
	}
}

func TestUUIDForPath(t *testing.T) {
	deps, _ := testDeps(t)
	listings := NewListings(deps)
	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/uuid",
		"/data/uuid?user="+testUser+"&path="+testHome+"/a.txt", listings.UUIDForPath)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}
