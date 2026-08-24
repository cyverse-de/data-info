package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/amqp"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/metadata"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/icattest"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/worker"
)

// operation is one method-and-path pair, in the Clojure swagger's spelling.
type operation struct{ Method, Path string }

func (o operation) String() string { return o.Method + " " + o.Path }

// movedToInfoTyper are the endpoints workstream C moved out of this service. They are absent
// on purpose and terrain calls info-typer for them.
var movedToInfoTyper = map[operation]string{
	{"GET", "/file-types"}:          "moved to info-typer",
	{"PUT", "/data/{data-id}/type"}: "moved to info-typer",
}

// notYetPorted are operations the Clojure service serves that this one does not.
//
// Every entry is a gap, not a decision. The list exists so that the gaps are counted and
// named rather than discovered at cutover, and so that a *new* gap fails this test instead
// of joining them silently. Removing an entry as it is ported is the point.
//
// Found by a route-level audit on 2026-08-21, after the shadow harness's error-code coverage
// reporting showed ERR_INVALID_PAGE, ERR_PAGE_NOT_POS and ERR_CHUNK_TOO_SMALL to be
// unreachable -- they are raised only by the tabular paging endpoints, which had not been
// written.
var notYetPorted = map[operation]string{
	// Phase 6, chunking and manifest. terrain calls all three by-id forms, which is what
	// backs file preview in the DE.
	{"GET", "/data/{data-id}/manifest"}:       "terrain routes/filesystem.clj GET /file/manifest",
	{"GET", "/data/{data-id}/chunks"}:         "terrain routes/filesystem.clj POST /read-chunk",
	{"GET", "/data/{data-id}/chunks-tabular"}: "terrain routes/filesystem.clj POST /read-tabular-chunk",

	{"GET", "/data/by-path/manifest/{path}"}:       "by-path form of the above; no caller found",
	{"GET", "/data/by-path/chunks/{path}"}:         "by-path form of the above; no caller found",
	{"GET", "/data/by-path/chunks-tabular/{path}"}: "by-path form of the above; no caller found",
}

// TestEveryClojureRouteIsServedOrAccountedFor compares what this service registers against
// the Clojure service's own /swagger.json, captured at phase 0 as the shape of record.
//
// A route-level audit found eleven unported operations that had gone unnoticed because
// nothing compared the two inventories -- three of them behind file preview in the DE, which
// a cutover would have broken outright. This test is that comparison, run every build.
func TestEveryClojureRouteIsServedOrAccountedFor(t *testing.T) {
	reference := clojureOperations(t)
	served := servedOperations(t)

	var undocumented []string
	for op := range reference {
		if served[op] {
			continue
		}
		if _, ok := movedToInfoTyper[op]; ok {
			continue
		}
		if _, ok := notYetPorted[op]; ok {
			continue
		}
		undocumented = append(undocumented, op.String())
	}
	sort.Strings(undocumented)

	for _, op := range undocumented {
		t.Errorf("%s is served by the Clojure service and not by this one, and is on neither "+
			"the moved-to-info-typer list nor the not-yet-ported list. Port it, or add it to "+
			"notYetPorted with the caller that needs it.", op)
	}

	// An entry that has been ported has to leave the list, or the list stops describing
	// the gap and starts hiding progress.
	for op := range notYetPorted {
		if served[op] {
			t.Errorf("%s is listed as not yet ported but is registered; remove it from notYetPorted", op)
		}
		if !reference[op] {
			t.Errorf("%s is listed as not yet ported but the Clojure service does not serve it either", op)
		}
	}
	for op := range movedToInfoTyper {
		if served[op] {
			t.Errorf("%s is listed as moved to info-typer but is still registered here", op)
		}
	}

	t.Logf("%d Clojure operations: %d served, %d moved to info-typer, %d not yet ported",
		len(reference), len(reference)-len(movedToInfoTyper)-len(notYetPorted),
		len(movedToInfoTyper), len(notYetPorted))
}

// clojureOperations reads the captured swagger document.
func clojureOperations(t *testing.T) map[operation]bool {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contract", "swagger-clojure.json"))
	if err != nil {
		t.Fatalf("reading the captured Clojure swagger: %v", err)
	}

	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the captured Clojure swagger: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("the captured Clojure swagger has no paths; the comparison would pass vacuously")
	}

	methods := map[string]bool{"get": true, "post": true, "put": true, "delete": true, "head": true, "patch": true}
	ops := map[operation]bool{}
	for path, byMethod := range doc.Paths {
		for method := range byMethod {
			if methods[strings.ToLower(method)] {
				ops[operation{strings.ToUpper(method), path}] = true
			}
		}
	}
	return ops
}

// echoParam rewrites echo's ":name" and "*" into the swagger document's "{name}" spelling.
var echoParam = regexp.MustCompile(`:([a-zA-Z][a-zA-Z0-9-]*)`)

// servedOperations asks the router what it actually registered, rather than reading the
// source. A route added behind a condition still counts, and one commented out does not.
func servedOperations(t *testing.T) map[operation]bool {
	t.Helper()

	// The data routes register only when the backends are present, so this needs a fully
	// wired server rather than testServer's status-only one. Nothing is dialed: the test
	// enumerates what was registered and issues no requests.
	e := testServerWithDeps(t, Deps{
		IRODSProbe: stubProber(nil),
		ICATProbe:  stubProber(nil),
		IRODS:      &irodsclient.Pool{},
		ICAT:       icattest.New(),
		Tasks:      &asynctasks.Client{},
		Worker:     &worker.Runner{},
		Notifier:   &notifications.Client{},
		Publisher:  &amqp.Publisher{},
		Metadata:   &metadata.Client{},
	})

	ops := map[operation]bool{}
	for _, r := range e.Routes() {
		path := echoParam.ReplaceAllString(r.Path, "{$1}")
		path = strings.ReplaceAll(path, "/*", "/{path}")
		if trimmed := strings.TrimRight(path, "/"); trimmed != "" {
			path = trimmed
		}
		ops[operation{strings.ToUpper(r.Method), path}] = true
	}
	return ops
}
