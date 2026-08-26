package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/labstack/echo/v4"
)

// TestManifest covers what a client switches on to decide how to render a file.
//
// The two cases turn on whether a deployment has an anonymous account at all. Whether that
// account can read a *particular* path is not exercised here: the in-memory catalog answers
// every user with the same access level, so it cannot tell the anonymous account apart from
// the caller. The shadow harness is where that distinction is checked.
func TestManifest(t *testing.T) {
	tests := []struct {
		name         string
		anonUser     string
		wantInfoType string
		wantURLs     []manifestURL
	}{
		{
			name:         "no anonymous account configured, so nowhere else to fetch it",
			anonUser:     "",
			wantInfoType: "unknown",
			wantURLs:     []manifestURL{},
		},
		{
			name:         "an anonymous account that can read reports where to fetch it",
			anonUser:     "anonymous",
			wantInfoType: "unknown",
			wantURLs: []manifestURL{
				{Label: "anonymous", URL: "https://anon.example/dl/home/wregglej/a.txt"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, fake := testDeps(t)
			deps.AnonUser = tt.anonUser
			deps.AnonBaseURL = "https://anon.example/dl"
			deps.AnonMappings = map[string]string{"/iplant/home": "home"}
			fake.AddUser("anonymous", icat.UserKindUser)
			fake.SetUUID("id-a", testHome+"/a.txt")

			rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/:data-id/manifest",
				"/data/id-a/manifest?user="+testUser, NewChunks(deps).Manifest)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}

			var got manifestResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
			}
			// From the name alone: this service does not sniff a file's contents.
			if got.ContentType != "text/plain" {
				t.Errorf("content-type = %q, want text/plain", got.ContentType)
			}
			if got.InfoType != tt.wantInfoType {
				t.Errorf("infoType = %q, want %q", got.InfoType, tt.wantInfoType)
			}
			if len(got.URLs) != len(tt.wantURLs) {
				t.Fatalf("urls = %v, want %v", got.URLs, tt.wantURLs)
			}
			for i := range tt.wantURLs {
				if got.URLs[i] != tt.wantURLs[i] {
					t.Errorf("urls[%d] = %v, want %v", i, got.URLs[i], tt.wantURLs[i])
				}
			}
		})
	}
}

// TestManifestByPathReadsTheWholeWildcard matters because the by-path routes carry the zone
// inside the wildcard rather than as a parameter of their own.
func TestManifestByPathReadsTheWholeWildcard(t *testing.T) {
	deps, _ := testDeps(t)
	chunks := NewChunks(deps)

	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/by-path/manifest/*",
		"/data/by-path/manifest/iplant/home/wregglej/a.txt?user="+testUser, chunks.ManifestByPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// TestChunkingRejectsWhatItCannotRead covers the validators these six routes share, and the
// parameter checks that raise codes of their own. Each fails before anything is read, which
// is why the reads themselves are not exercised here.
func TestChunkingRejectsWhatItCannotRead(t *testing.T) {
	deps, fake := testDeps(t)
	fake.SetUUID("id-a", testHome+"/a.txt")
	fake.SetUUID("id-home", testHome)
	chunks := NewChunks(deps)

	tests := []struct {
		name     string
		pattern  string
		target   string
		handler  echo.HandlerFunc
		wantCode apierror.Code
	}{
		{
			name: "an id that names nothing", pattern: "/data/:data-id/manifest",
			target:  "/data/id-missing/manifest?user=" + testUser,
			handler: chunks.Manifest, wantCode: apierror.ErrDoesNotExist,
		},
		{
			name: "a folder is not a file", pattern: "/data/:data-id/manifest",
			target:  "/data/id-home/manifest?user=" + testUser,
			handler: chunks.Manifest, wantCode: apierror.ErrNotAFile,
		},
		{
			name: "a path that is not there", pattern: "/data/by-path/chunks/*",
			target:  "/data/by-path/chunks/iplant/home/wregglej/missing.txt?user=" + testUser + "&position=0&size=10",
			handler: chunks.ChunkByPath, wantCode: apierror.ErrDoesNotExist,
		},
		{
			name: "a caller iRODS has never heard of", pattern: "/data/:data-id/chunks",
			target:  "/data/id-a/chunks?user=nobody&position=0&size=10",
			handler: chunks.Chunk, wantCode: apierror.ErrNotAUser,
		},
		{
			name: "page zero", pattern: "/data/:data-id/chunks-tabular",
			target:  "/data/id-a/chunks-tabular?user=" + testUser + "&separator=%2C&page=0&size=10",
			handler: chunks.TabularChunk, wantCode: apierror.ErrPageNotPos,
		},
		{
			name: "a chunk size of nothing", pattern: "/data/:data-id/chunks-tabular",
			target:  "/data/id-a/chunks-tabular?user=" + testUser + "&separator=%2C&page=1&size=0",
			handler: chunks.TabularChunk, wantCode: apierror.ErrChunkTooSmall,
		},
		{
			name: "a page past the end", pattern: "/data/:data-id/chunks-tabular",
			// 100 bytes at 10 to a page is ten pages, and the bound is inclusive, so the
			// first page it refuses is the twelfth.
			target:  "/data/id-a/chunks-tabular?user=" + testUser + "&separator=%2C&page=12&size=10",
			handler: chunks.TabularChunk, wantCode: apierror.ErrInvalidPage,
		},
		{
			name: "a missing separator", pattern: "/data/:data-id/chunks-tabular",
			target:  "/data/id-a/chunks-tabular?user=" + testUser + "&page=1&size=10",
			handler: chunks.TabularChunk, wantCode: apierror.ErrIllegalArgument,
		},
		// The paging checks run before the caller and the path are validated, because the
		// Clojure service puts them in a pre-hook that fires ahead of the function body. Both
		// of these would report the validator's code if the order were the other way round.
		{
			name: "page zero against a path that is not there", pattern: "/data/by-path/chunks-tabular/*",
			target: "/data/by-path/chunks-tabular/iplant/home/wregglej/missing.csv?user=" + testUser +
				"&separator=%2C&page=0&size=10",
			handler: chunks.TabularChunkByPath, wantCode: apierror.ErrPageNotPos,
		},
		{
			name: "a chunk size of nothing for a caller who does not exist", pattern: "/data/:data-id/chunks-tabular",
			target:  "/data/id-a/chunks-tabular?user=nobody&separator=%2C&page=1&size=0",
			handler: chunks.TabularChunk, wantCode: apierror.ErrChunkTooSmall,
		},
		{
			name: "a missing position", pattern: "/data/:data-id/chunks",
			target:  "/data/id-a/chunks?user=" + testUser + "&size=10",
			handler: chunks.Chunk, wantCode: apierror.ErrIllegalArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, tt.pattern, tt.target, tt.handler)

			var envelope map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
			}
			if envelope["error_code"] != string(tt.wantCode) {
				t.Errorf("error_code = %v, want %s (%s)", envelope["error_code"], tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestTabularSeparatorMayBeWhitespace is the regression test for a tab-delimited preview.
//
// echo percent-decodes a query value before a handler sees it, so ?separator=%09 arrives as a
// tab. Reading it through a helper that rejects blank strings turned every TSV and every
// space-delimited file into a 400, where the Clojure service serves them: the parameter is
// declared s/Str, not NonBlankString, and the endpoint's own documentation names %09 as the
// value to send for a tab.
//
// The assertion is indirect on purpose. Reading the file needs iRODS, so the case asks for
// page zero as well: reaching ERR_PAGE_NOT_POS proves the separator was accepted, since a
// rejected one answers ERR_ILLEGAL_ARGUMENT before the paging checks run.
func TestTabularSeparatorMayBeWhitespace(t *testing.T) {
	deps, fake := testDeps(t)
	fake.SetUUID("id-a", testHome+"/a.txt")
	chunks := NewChunks(deps)

	tests := []struct {
		name      string
		separator string
	}{
		{"a tab, which is what a TSV preview sends", "%09"},
		{"a space", "%20"},
		{"a comma, the ordinary case", "%2C"},
		{"a doubly-encoded tab, which decodes twice as the Clojure service does", "%2509"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/:data-id/chunks-tabular",
				"/data/id-a/chunks-tabular?user="+testUser+"&separator="+tt.separator+"&page=0&size=10",
				chunks.TabularChunk)

			var envelope map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
			}
			if envelope["error_code"] != string(apierror.ErrPageNotPos) {
				t.Errorf("error_code = %v, want %s -- the separator was rejected (%s)",
					envelope["error_code"], apierror.ErrPageNotPos, rec.Body.String())
			}
		})
	}
}
