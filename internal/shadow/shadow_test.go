package shadow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
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
		switch c.Tier {
		case TierRead, TierWrite, TierAsync:
		default:
			// A misspelled tier would be skipped at run time with a reason that reads
			// like a limitation rather than a typo, so the case would quietly stop
			// being checked.
			t.Errorf("case %q has tier %q, which is not a tier", c.ID, c.Tier)
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

// TestTaskIDFrom covers the shapes an async endpoint's response actually takes. An id that
// is missed means the harness reads the tree while the job is still writing it, and an id
// invented from a malformed body means it waits for a task that does not exist.
func TestTaskIDFrom(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"a move", `{"user":"u","sources":["/a"],"dest":"/b","async-task-id":"abc-123"}`, "abc-123"},
		{"nothing to do", `{"user":"u","source":"/a","dest":"/a"}`, ""},
		{"an error envelope", `{"error_code":"ERR_DOES_NOT_EXIST","path":"/a"}`, ""},
		{"not JSON at all", `<html>502</html>`, ""},
		{"an empty body", ``, ""},
		{"a JSON array", `[{"async-task-id":"abc-123"}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskIDFrom([]byte(tc.body)); got != tc.want {
				t.Errorf("taskIDFrom(%s) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestStatusTrailComparesSequenceNotTiming pins what the async tier actually asserts about a
// task's history: the order of the statuses and their details, with the paths inside them
// canonicalised across the paired fixtures, and nothing about when they happened.
func TestStatusTrailComparesSequenceNotTiming(t *testing.T) {
	at := func(s string) *time.Time {
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parsing %q: %v", s, err)
		}
		return &parsed
	}
	trail := func(root string, when string, statuses ...[2]string) *asynctasks.Task {
		task := &asynctasks.Task{EndDate: at(when)}
		for _, s := range statuses {
			task.Statuses = append(task.Statuses, asynctasks.Status{
				Status:      s[0],
				Detail:      strings.ReplaceAll(s[1], "{ROOT}", root),
				CreatedDate: *at(when),
			})
		}
		return task
	}

	n := NewNormalizer("RUN")
	sequence := [][2]string{
		{"registered", ""},
		{"running", "moving {ROOT}/movable.txt"},
		{"completed", ""},
	}

	// Same sequence, opposite sides of the paired fixture, hours apart.
	reference := trail("/z/scratch/RUN/A", "2026-08-21T10:00:00Z", sequence...)
	candidate := trail("/z/scratch/RUN/B", "2026-08-21T13:31:07Z", sequence...)

	if diffs := n.Compare(
		Response{Status: 200, Body: statusTrail(reference)},
		Response{Status: 200, Body: statusTrail(candidate)},
	); len(diffs) != 0 {
		t.Errorf("identical trails on opposite sides differed: %+v", diffs)
	}

	// A status the other side never reported has to show up.
	shortened := trail("/z/scratch/RUN/B", "2026-08-21T10:00:00Z", sequence[:2]...)
	if diffs := n.Compare(
		Response{Status: 200, Body: statusTrail(reference)},
		Response{Status: 200, Body: statusTrail(shortened)},
	); len(diffs) == 0 {
		t.Error("a missing terminal status produced no difference")
	}

	// So does the same sequence in the wrong order: terrain's poller reads it in order.
	reordered := trail("/z/scratch/RUN/B", "2026-08-21T10:00:00Z", sequence[1], sequence[0], sequence[2])
	if diffs := n.Compare(
		Response{Status: 200, Body: statusTrail(reference)},
		Response{Status: 200, Body: statusTrail(reordered)},
	); len(diffs) == 0 {
		t.Error("a reordered status trail produced no difference")
	}
}

// TestCatalogRejectsMalformedSteps pins the setup-step validation. Each of these would
// otherwise fail at run time, in the middle of a run, with an error that reads like a
// service difference rather than a catalog mistake.
func TestCatalogRejectsMalformedSteps(t *testing.T) {
	for _, tc := range []struct {
		name string
		step string
		want string
	}{
		{
			name: "both seeds and sends",
			step: "      - {seed: {filename: a.txt, content: \"a\"}, method: POST, path: /deleter}",
			want: "both seeds and sends a request",
		},
		{
			name: "does neither",
			step: "      - {await: true}",
			want: "neither seeds nor sends a request",
		},
		{
			name: "awaits a seed",
			step: "      - {seed: {filename: a.txt, content: \"a\"}, await: true}",
			want: "awaits a seed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			doc := "group: g\ncases:\n  - id: c\n    tier: async\n    method: POST\n    path: /restorer\n    before:\n" + tc.step + "\n"
			if err := os.WriteFile(filepath.Join(dir, "g.yaml"), []byte(doc), 0o600); err != nil {
				t.Fatalf("writing the case file: %v", err)
			}

			_, err := LoadCatalog(dir)
			if err == nil {
				t.Fatal("LoadCatalog accepted a malformed step")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestStepExpandsPerSide is the property the multi-step model rests on: a step is expanded
// with the vars of the side it runs against, so each service's setup touches its own copy of
// the fixture and never the other's.
func TestStepExpandsPerSide(t *testing.T) {
	step := Step{
		Method: "POST",
		Path:   "/deleter",
		Query:  map[string]string{"user": "{{.User}}"},
		Body:   map[string]any{"paths": []any{"{{.Root}}/doomed.txt"}},
	}

	for _, side := range []string{"A", "B"} {
		root := "/z/scratch/RUN/" + side
		got := step.Expand(map[string]string{"User": "someone", "Root": root})

		if got.Query["user"] != "someone" {
			t.Errorf("side %s: query user = %q", side, got.Query["user"])
		}
		paths, ok := got.Body.(map[string]any)["paths"].([]any)
		if !ok || len(paths) != 1 {
			t.Fatalf("side %s: body did not survive expansion: %#v", side, got.Body)
		}
		if want := root + "/doomed.txt"; paths[0] != want {
			t.Errorf("side %s: path = %q, want %q", side, paths[0], want)
		}
	}

	// The original is untouched, so the second side does not expand an already-expanded
	// template and quietly point at the first side's tree.
	if step.Path != "/deleter" || step.Body.(map[string]any)["paths"].([]any)[0] != "{{.Root}}/doomed.txt" {
		t.Error("Expand mutated the step it was given")
	}
}

// TestReportNamesUnelicitedCodes covers the cutover gate that every error code data-info can
// return is exercised at least once. The report is where that stops being an assertion and
// becomes a measurement, so it has to name what is missing rather than only counting.
func TestReportCodeCoverage(t *testing.T) {
	all := apierror.EmittedCodes()

	t.Run("nothing elicited", func(t *testing.T) {
		var out strings.Builder
		if _, err := Report(&out, []Result{{Case: Case{ID: "a"}}}); err != nil {
			t.Fatalf("Report: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, fmt.Sprintf("error codes elicited: 0 of %d", len(all))) {
			t.Errorf("report did not count zero coverage:\n%s", got)
		}
		// Naming them is the point: a count alone tells nobody which case to write.
		for _, c := range all {
			if !strings.Contains(got, string(c)) {
				t.Errorf("report did not name the unelicited code %s", c)
			}
		}
	})

	t.Run("one elicited", func(t *testing.T) {
		var out strings.Builder
		results := []Result{{Case: Case{ID: "a"}, Code: string(apierror.ErrDoesNotExist)}}
		if _, err := Report(&out, results); err != nil {
			t.Fatalf("Report: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, fmt.Sprintf("error codes elicited: 1 of %d", len(all))) {
			t.Errorf("report did not count the elicited code:\n%s", got)
		}
		missing := got[strings.Index(got, "not elicited by any case:"):]
		if strings.Contains(missing, string(apierror.ErrDoesNotExist)) {
			t.Error("an elicited code was still listed as missing")
		}
	})

	t.Run("coverage does not decide the verdict", func(t *testing.T) {
		var out strings.Builder
		// A run with no differences passes even though it elicited nothing: coverage is
		// a property of the catalog, not a disagreement between the services.
		matched, err := Report(&out, []Result{{Case: Case{ID: "a"}}})
		if err != nil {
			t.Fatalf("Report: %v", err)
		}
		if !matched {
			t.Error("a run with no diffs failed because of missing code coverage")
		}
	})
}

// TestErrorCodeFrom keeps the observation honest: a body that is not an error envelope must
// not be recorded as covering anything.
func TestErrorCodeFrom(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"an error envelope", `{"error_code":"ERR_DOES_NOT_EXIST","path":"/a"}`, "ERR_DOES_NOT_EXIST"},
		{"a success body", `{"id":"abc","path":"/a"}`, ""},
		{"not JSON", `<html>502</html>`, ""},
		{"empty", ``, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorCodeFrom([]byte(tc.body)); got != tc.want {
				t.Errorf("errorCodeFrom(%s) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}
