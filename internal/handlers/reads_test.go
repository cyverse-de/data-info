package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/icattest"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/labstack/echo/v4"
)

const (
	testZone = "iplant"
	testUser = "wregglej"
	testHome = "/iplant/home/wregglej"
)

func testDeps(t *testing.T) (Deps, *icattest.Fake) {
	t.Helper()

	fake := icattest.New()
	fake.AddUser(testUser, icat.UserKindUser)
	fake.AddCollection(testHome, icat.AccessOwn)
	fake.AddDataObject(testHome+"/a.txt", 100, icat.AccessRead)

	pool, err := irodsclient.NewPool(irodsclient.Config{
		Host: "irods.invalid", Port: 1247, Zone: testZone, ProxyUser: "rods",
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	return Deps{
		ICAT:              fake,
		IRODS:             pool,
		Layout:            paths.Layout{Zone: testZone, Home: "/iplant/home", CommunityData: "/iplant/home/shared"},
		ProxyUser:         "rods",
		MaxPathsInRequest: 3,
		PermsFilter:       map[string]bool{"rods": true},
	}, fake
}

// serve runs one request through a router wired the way the service wires it, so the tests
// exercise the real error handler and the real per-route error style.
func serve(t *testing.T, style apierror.Style, method, target, body string, handler echo.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = apierror.HTTPErrorHandler(nil)

	middleware := []echo.MiddlewareFunc{}
	if style != apierror.StyleTrap {
		middleware = append(middleware, apierror.WithStyle(style))
	}
	e.Add(method, "/*", handler, middleware...)

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestExistenceMarker(t *testing.T) {
	deps, _ := testDeps(t)
	reads := NewReads(deps)

	body := `{"paths":["` + testHome + `","` + testHome + `/a.txt","` + testHome + `/missing"]}`
	rec := serve(t, apierror.StyleOK, http.MethodPost, "/existence-marker?user="+testUser, body, reads.Existence)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Paths map[string]bool `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	tests := []struct {
		path string
		want bool
	}{
		{testHome, true},
		{testHome + "/a.txt", true},
		{testHome + "/missing", false},
	}
	for _, tt := range tests {
		if got := resp.Paths[tt.path]; got != tt.want {
			t.Errorf("%s = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// TestBulkLimitIsEnforced covers the guard on every bulk endpoint. Without it a caller
// could ask for arbitrarily many paths and fan out into that many catalog queries.
func TestBulkLimitIsEnforced(t *testing.T) {
	deps, _ := testDeps(t) // limit is 3
	reads := NewReads(deps)

	body := `{"paths":["/a","/b","/c","/d"]}`
	rec := serve(t, apierror.StyleOK, http.MethodPost, "/existence-marker?user="+testUser, body, reads.Existence)

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if envelope["error_code"] != string(apierror.ErrTooManyResults) {
		t.Errorf("error_code = %v, want %s", envelope["error_code"], apierror.ErrTooManyResults)
	}
	if envelope["count"] != float64(4) || envelope["limit"] != float64(3) {
		t.Errorf("count/limit = %v/%v, want 4/3", envelope["count"], envelope["limit"])
	}
}

// TestMissingUserParameter covers the only identity the service has.
//
// It answers ERR_ILLEGAL_ARGUMENT with a 400, not ERR_MISSING_QUERY_PARAMETER. Every
// endpoint declares user as a required non-blank parameter, so the Clojure stack rejects the
// request during schema validation before the handler is reached, and validation failures
// are always reported that way whatever the endpoint's own error style. Verified against the
// running service.
func TestMissingUserParameter(t *testing.T) {
	deps, _ := testDeps(t)
	reads := NewReads(deps)

	rec := serve(t, apierror.StyleOK, http.MethodPost, "/existence-marker", `{"paths":["/a"]}`, reads.Existence)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if envelope["error_code"] != string(apierror.ErrIllegalArgument) {
		t.Errorf("error_code = %v, want %s", envelope["error_code"], apierror.ErrIllegalArgument)
	}
}

func TestPermissionsGatherer(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddPerm(testHome, testUser, testZone, icat.AccessOwn)
	fake.AddPerm(testHome, "someone", testZone, icat.AccessRead)
	fake.AddPerm(testHome, "rods", testZone, icat.AccessOwn)

	reads := NewReads(deps)
	rec := serve(t, apierror.StyleTrap, http.MethodPost, "/permissions-gatherer?user="+testUser,
		`{"paths":["`+testHome+`"]}`, reads.Permissions)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Paths []pathPermissions `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(resp.Paths) != 1 {
		t.Fatalf("got %d paths, want 1", len(resp.Paths))
	}

	// The requesting user and the filtered service account are both left out; reporting
	// the proxy account would tell every user their data is shared with an account they
	// have never heard of.
	perms := resp.Paths[0].UserPermissions
	if len(perms) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(perms), perms)
	}
	if perms[0].User != "someone" || perms[0].Permission != "read" {
		t.Errorf("entry = %+v, want someone/read", perms[0])
	}
}

// TestPermissionsRequiresOwnership is the authorisation for the endpoint. The catalog query
// behind it returns a path's full access list to whoever asks.
func TestPermissionsRequiresOwnership(t *testing.T) {
	deps, _ := testDeps(t)
	reads := NewReads(deps)

	// a.txt is readable, not owned.
	rec := serve(t, apierror.StyleTrap, http.MethodPost, "/permissions-gatherer?user="+testUser,
		`{"paths":["`+testHome+`/a.txt"]}`, reads.Permissions)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if envelope["error_code"] != string(apierror.ErrNotOwner) {
		t.Errorf("error_code = %v, want %s", envelope["error_code"], apierror.ErrNotOwner)
	}
}

// TestErrorStyleChangesTheStatus pins the distinction the port has to preserve: the same
// error_code answers 403 on a trap-style route and 500 on an ok-style one.
func TestErrorStyleChangesTheStatus(t *testing.T) {
	deps, _ := testDeps(t)
	reads := NewReads(deps)
	body := `{"paths":["` + testHome + `/a.txt"]}`

	trap := serve(t, apierror.StyleTrap, http.MethodPost, "/x?user="+testUser, body, reads.Permissions)
	if trap.Code != http.StatusForbidden {
		t.Errorf("trap-style status = %d, want 403", trap.Code)
	}

	ok := serve(t, apierror.StyleOK, http.MethodPost, "/x?user="+testUser, body, reads.Permissions)
	if ok.Code != http.StatusInternalServerError {
		t.Errorf("ok-style status = %d, want 500", ok.Code)
	}

	// Same code either way; only the status differs.
	for name, rec := range map[string]*httptest.ResponseRecorder{"trap": trap, "ok": ok} {
		var envelope map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s: decoding: %v", name, err)
		}
		if envelope["error_code"] != string(apierror.ErrNotOwner) {
			t.Errorf("%s: error_code = %v, want %s", name, envelope["error_code"], apierror.ErrNotOwner)
		}
	}
}

func TestBasePaths(t *testing.T) {
	deps, _ := testDeps(t)
	reads := NewReads(deps)

	rec := serve(t, apierror.StyleTrap, http.MethodGet, "/navigation/base-paths?user="+testUser, "", reads.BasePaths)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	want := map[string]string{
		"user_home_path":  "/iplant/home/wregglej",
		"user_trash_path": "/iplant/trash/home/wregglej",
		"base_trash_path": "/iplant/trash/home",
	}
	for key, expected := range want {
		if resp[key] != expected {
			t.Errorf("%s = %q, want %q", key, resp[key], expected)
		}
	}
}

// TestUnknownCallerIsRejected covers the validation every endpoint does first. Without it an
// unknown user resolves to no groups, every catalog row is filtered out, and the service
// answers as though nothing exists rather than saying who the caller is.
func TestUnknownCallerIsRejected(t *testing.T) {
	deps, _ := testDeps(t)
	reads := NewReads(deps)

	rec := serve(t, apierror.StyleOK, http.MethodPost, "/existence-marker?user=nosuchuser",
		`{"paths":["`+testHome+`"]}`, reads.Existence)

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if envelope["error_code"] != string(apierror.ErrNotAUser) {
		t.Errorf("error_code = %v, want %s", envelope["error_code"], apierror.ErrNotAUser)
	}
	// Singular here: this endpoint validates through the jargon validators, whose envelope
	// names one user. The stat endpoints validate through clj-irods and report a list.
	if envelope["user"] != "nosuchuser" {
		t.Errorf("user = %v, want nosuchuser", envelope["user"])
	}
}

// TestGroupIsNotAUser guards a distinction the catalog makes and a naive existence check
// would not: sharing with a group is a different operation.
func TestGroupIsNotAUser(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddUser("some-group", icat.UserKindGroup)
	reads := NewReads(deps)

	rec := serve(t, apierror.StyleOK, http.MethodPost, "/existence-marker?user=some-group",
		`{"paths":["`+testHome+`"]}`, reads.Existence)

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if envelope["error_code"] != string(apierror.ErrNotAUser) {
		t.Errorf("a group satisfied a user check: %v", envelope)
	}
}

// TestExistenceRequiresReadable covers the difference between an object being in the catalog
// and the caller being able to read it. iRODS has access levels between none and read.
func TestExistenceRequiresReadable(t *testing.T) {
	deps, fake := testDeps(t)
	// delete_object sits above write in iRODS' numbering but is not one of the three
	// levels the DE recognises, so it reports as no permission.
	fake.AddDataObject(testHome+"/odd.txt", 1, 1130)
	reads := NewReads(deps)

	rec := serve(t, apierror.StyleOK, http.MethodPost, "/existence-marker?user="+testUser,
		`{"paths":["`+testHome+`/odd.txt"]}`, reads.Existence)

	var resp struct {
		Paths map[string]bool `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if resp.Paths[testHome+"/odd.txt"] {
		t.Error("a path the caller cannot read reported as existing")
	}
}

// TestPermissionsReportsEveryMissingPath covers both halves of the validator ordering: all
// missing paths are reported, and existence is checked across the whole list before
// ownership.
func TestPermissionsReportsEveryMissingPath(t *testing.T) {
	deps, _ := testDeps(t)
	reads := NewReads(deps)

	// The first path is readable but not owned, the rest are missing. The reference runs
	// the existence check over the whole list first, so the missing ones win.
	body := `{"paths":["` + testHome + `/a.txt","` + testHome + `/gone1","` + testHome + `/gone2"]}`
	rec := serve(t, apierror.StyleTrap, http.MethodPost, "/permissions-gatherer?user="+testUser, body, reads.Permissions)

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if envelope["error_code"] != string(apierror.ErrDoesNotExist) {
		t.Fatalf("error_code = %v, want %s", envelope["error_code"], apierror.ErrDoesNotExist)
	}

	missing, ok := envelope["paths"].([]any)
	if !ok || len(missing) != 2 {
		t.Errorf("paths = %v, want both missing paths", envelope["paths"])
	}
}

// TestPermissionsToleratesATrailingSlash covers a silent wrong answer: the scope keys its
// results by the catalog's canonical path, so looking an access list up by the caller's
// spelling returned an empty list for a path that was in fact shared.
func TestPermissionsToleratesATrailingSlash(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddPerm(testHome, "someone", testZone, icat.AccessRead)
	reads := NewReads(deps)

	rec := serve(t, apierror.StyleTrap, http.MethodPost, "/permissions-gatherer?user="+testUser,
		`{"paths":["`+testHome+`/"]}`, reads.Permissions)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Paths []pathPermissions `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(resp.Paths) != 1 || len(resp.Paths[0].UserPermissions) != 1 {
		t.Errorf("a trailing slash lost the access list: %+v", resp.Paths)
	}
}
