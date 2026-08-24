package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50", listings.FolderListing)

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
				"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50&"+tt.query, listings.FolderListing)

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

// TestListingRejectsBadSortParameters covers the values the paging schema declares as enums.
// A sort-field outside the enum is a deliberate improvement over the reference, which lets
// it reach a bare exception and answers 500 with no code; a sort-dir outside it is a plain
// match, since ring-swagger rejects the request before the handler runs.
func TestListingRejectsBadSortParameters(t *testing.T) {
	deps, _ := testDeps(t)
	listings := NewListings(deps)

	tests := []struct {
		name  string
		query string
	}{
		{"unsortable field", "sort-field=nonsense"},
		{"catalog column rather than the API name", "sort-field=base_name"},
		{"lowercase direction", "sort-field=name&sort-dir=desc"},
		{"unknown direction", "sort-field=name&sort-dir=sideways"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
				"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50&"+tt.query,
				listings.FolderListing)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
		})
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

			rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
				tt.target+"?user="+testUser+"&limit=50", listings.FolderListing)

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
		// A missing user is a schema failure, so a 400. The 422 above is for a user that
		// parses but does not exist, which is a different answer.
		{"no user", "/data/11111111-2222-3333-4444-555555555555", http.StatusBadRequest},
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

// TestListingRequiresLimit covers a parameter the reference makes mandatory. Defaulting it
// would silently hand a caller the first page of an arbitrarily large folder with no way to
// know more existed.
func TestListingRequiresLimit(t *testing.T) {
	deps, _ := testDeps(t)
	listings := NewListings(deps)

	// Trap-style, as the data routes are in the reference, so the status table applies.
	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser, listings.FolderListing)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if envelope["error_code"] != string(apierror.ErrMissingQueryParam) {
		t.Errorf("error_code = %v, want %s", envelope["error_code"], apierror.ErrMissingQueryParam)
	}
	if envelope["parameters"] != "limit" {
		t.Errorf("parameters = %v, want limit", envelope["parameters"])
	}
}

// TestEntityTypeSelectsWhatIsListed covers a parameter that was being dropped, which made a
// folders-only listing return files as well and report a total counting both.
func TestEntityTypeSelectsWhatIsListed(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddCollection(testHome+"/sub", icat.AccessOwn)
	listings := NewListings(deps)

	tests := []struct {
		entityType  string
		wantFiles   int
		wantFolders int
	}{
		{"", 1, 1},
		{"any", 1, 1},
		{"file", 1, 0},
		{"folder", 0, 1},
	}

	for _, tt := range tests {
		t.Run("entity-type="+tt.entityType, func(t *testing.T) {
			rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
				"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50&entity-type="+tt.entityType,
				listings.FolderListing)

			var listing service.Listing
			if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
				t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
			}
			if len(listing.Files) != tt.wantFiles {
				t.Errorf("files = %d, want %d", len(listing.Files), tt.wantFiles)
			}
			if len(listing.Folders) != tt.wantFolders {
				t.Errorf("folders = %d, want %d", len(listing.Folders), tt.wantFolders)
			}
		})
	}
}

// TestBadNameFlaggingComesFromTheRequest guards against the service's own bad-chars setting
// leaking into listings. That setting governs what may be used in a new name; applying it
// here would flag existing files that display perfectly well.
func TestBadNameFlaggingComesFromTheRequest(t *testing.T) {
	deps, fake := testDeps(t)
	deps.BadChars = "'"
	fake.AddDataObject(testHome+"/it's.txt", 1, icat.AccessRead)
	listings := NewListings(deps)

	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50", listings.FolderListing)

	var listing service.Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
	}
	for _, f := range listing.Files {
		if f.Name == "it's.txt" && f.BadName {
			t.Error("a name was flagged from the service default rather than the request")
		}
	}

	// Asked for explicitly, it is flagged.
	rec = serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50&bad-chars=%27", listings.FolderListing)
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	var flagged bool
	for _, f := range listing.Files {
		if f.Name == "it's.txt" {
			flagged = f.BadName
		}
	}
	if !flagged {
		t.Error("a name the caller asked about was not flagged")
	}
}

// TestReadmeIsReported covers a field that was hard-coded false, which would have been a
// silent regression for the UI that renders it.
func TestReadmeIsReported(t *testing.T) {
	deps, fake := testDeps(t)
	listings := NewListings(deps)

	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50", listings.FolderListing)
	var listing service.Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if listing.Readme != false {
		t.Errorf("readme = %v, want false when there is none", listing.Readme)
	}

	fake.AddDataObject(testHome+"/README.md", 12, icat.AccessRead)

	rec = serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej?user="+testUser+"&limit=50", listings.FolderListing)
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	entry, ok := listing.Readme.(map[string]any)
	if !ok {
		t.Fatalf("readme = %v, want the entry", listing.Readme)
	}
	if entry["name"] != "README.md" {
		t.Errorf("readme name = %v, want README.md", entry["name"])
	}
}

// TestZoneSharingARouteName covers a path-parsing bug: searching the whole URL for the zone
// name finds the route prefix instead when the two coincide.
func TestZoneSharingARouteName(t *testing.T) {
	deps, fake := testDeps(t)
	deps.Layout.Zone = "data"
	fake.AddCollection("/data/home/wregglej", icat.AccessOwn)
	listings := NewListings(deps)

	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/data/home/wregglej?user="+testUser+"&limit=50", listings.FolderListing)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (%s); the zone was resolved against the route prefix",
			rec.Code, rec.Body.String())
	}
}

// "unknown" is both a request for objects with no info type and a value to match, because
// info-typer records that literal string on a file it cannot identify. Dropping it from the
// match list made a folder of untyped files list as empty.
func TestInfoTypeFilter(t *testing.T) {
	cases := []struct {
		name        string
		query       string
		wantTypes   []string
		wantUnknown bool
	}{
		{
			name:  "no parameter filters nothing",
			query: "/x",
		},
		{
			name:      "a single type",
			query:     "/x?info-type=csv",
			wantTypes: []string{"csv"},
		},
		{
			name:      "a comma-separated list",
			query:     "/x?info-type=csv,tsv",
			wantTypes: []string{"csv", "tsv"},
		},
		{
			name:      "repeated parameters accumulate",
			query:     "/x?info-type=csv&info-type=tsv",
			wantTypes: []string{"csv", "tsv"},
		},
		{
			name:        "unknown asks for untyped objects and matches the literal value",
			query:       "/x?info-type=unknown",
			wantTypes:   []string{"unknown"},
			wantUnknown: true,
		},
		{
			name:        "unknown alongside a real type",
			query:       "/x?info-type=csv,unknown",
			wantTypes:   []string{"csv", "unknown"},
			wantUnknown: true,
		},
		{
			name:        "the spelling of unknown does not matter",
			query:       "/x?info-type=UNKNOWN",
			wantTypes:   []string{"UNKNOWN"},
			wantUnknown: true,
		},
		{
			name:      "blank entries are ignored",
			query:     "/x?info-type=,csv,",
			wantTypes: []string{"csv"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, tc.query, nil)
			c := e.NewContext(req, httptest.NewRecorder())

			types, unknown := infoTypeFilter(c)

			if strings.Join(types, ",") != strings.Join(tc.wantTypes, ",") {
				t.Errorf("types = %q, want %q", types, tc.wantTypes)
			}
			if unknown != tc.wantUnknown {
				t.Errorf("includeUnknown = %v, want %v", unknown, tc.wantUnknown)
			}
		})
	}
}
