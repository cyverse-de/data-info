package shadow

import (
	"net/http"
	"strings"
	"testing"
)

func headers(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Add(pairs[i], pairs[i+1])
	}
	return h
}

// kindsOf lists the diff kinds a comparison produced, which is what the cases assert on.
func kindsOf(diffs []Diff) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Kind)
	}
	return out
}

// TestHeaderComparison covers the classification, which is the part that has to be right:
// a contract header is compared exactly, stack noise is ignored, and anything the
// normalizer has no opinion about is reported rather than skipped.
func TestHeaderComparison(t *testing.T) {
	n := NewNormalizer("run")

	tests := []struct {
		name      string
		reference http.Header
		candidate http.Header
		want      []string
	}{
		{
			name:      "identical contract headers agree",
			reference: headers("Content-Type", "application/json;charset=utf-8"),
			candidate: headers("Content-Type", "application/json;charset=utf-8"),
			want:      nil,
		},
		{
			name:      "a differing content type is a difference",
			reference: headers("Content-Type", "application/json;charset=utf-8"),
			candidate: headers("Content-Type", "application/json"),
			want:      []string{"header:Content-Type"},
		},
		{
			// The reference omits the content type entirely on its default error path, so
			// absent and empty have to be distinguishable.
			name:      "a content type present on one side only is a difference",
			reference: headers(),
			candidate: headers("Content-Type", "application/json"),
			want:      []string{"header:Content-Type"},
		},
		{
			name:      "stack noise is ignored even when it differs",
			reference: headers("Date", "Mon, 24 Aug 2026 16:00:00 GMT", "Server", "Jetty(12.1.8)", "Content-Length", "81"),
			candidate: headers("Date", "Mon, 24 Aug 2026 16:00:01 GMT", "Transfer-Encoding", "chunked"),
			want:      nil,
		},
		{
			name:      "a header only the reference sends is reported",
			reference: headers("X-Frame-Options", "DENY"),
			candidate: headers(),
			want:      []string{"header:unclassified"},
		},
		{
			// The case reference-only discovery is blind to: a Go stack adds headers a
			// Ring stack never sent, and every one is a wire change.
			name:      "a header only the candidate sends is reported",
			reference: headers(),
			candidate: headers("X-Content-Type-Options", "nosniff"),
			want:      []string{"header:unclassified"},
		},
		{
			name:      "an unclassified header is reported even when both sides agree",
			reference: headers("Vary", "Accept"),
			candidate: headers("Vary", "Accept"),
			want:      []string{"header:unclassified"},
		},
		{
			name:      "the download filename is contract",
			reference: headers("Content-Disposition", `attachment; filename="a.txt"`),
			candidate: headers("Content-Disposition", `inline; filename="a.txt"`),
			want:      []string{"header:Content-Disposition"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kindsOf(n.compareHeaders(tt.reference, tt.candidate))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("kinds = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCompareReportsHeaderAndBodyTogether pins that a non-JSON body, which is compared as
// raw bytes, does not cost the response its header comparison.
func TestCompareReportsHeaderAndBodyTogether(t *testing.T) {
	n := NewNormalizer("run")

	diffs := n.Compare(
		Response{Status: 200, Body: []byte("not json"), Headers: headers("Content-Type", "text/plain")},
		Response{Status: 200, Body: []byte("also not json"), Headers: headers("Content-Type", "application/octet-stream")},
	)

	var sawHeader, sawBody bool
	for _, d := range diffs {
		switch d.Kind {
		case "header:Content-Type":
			sawHeader = true
		case "body":
			sawBody = true
		}
	}
	if !sawHeader || !sawBody {
		t.Errorf("kinds = %v, want both a header and a body difference", kindsOf(diffs))
	}
}

// TestRunVaryingValuesAreCanonicalised covers the values two services legitimately disagree
// about because each is reporting something true of itself, or because the value is random
// by design. Both were reported as differences by the first paired run against QA.
func TestRunVaryingValuesAreCanonicalised(t *testing.T) {
	n := NewNormalizer("run1")

	tests := []struct {
		name                 string
		reference, candidate string
	}{
		{
			name:      "an async task's detail names the pod that ran it",
			reference: `{"detail":"[data-info-8485cb7d8b-t6dcz]","status":"running"}`,
			candidate: `{"detail":"[data-info-next-d66b6b687-c7pf5]","status":"running"}`,
		},
		{
			name:      "the trash suffix is random by design",
			reference: `{"p":"/cyverse/trash/home/wregglej/doomed.txt.1gRkrCx"}`,
			candidate: `{"p":"/cyverse/trash/home/wregglej/doomed.txt.m3cPtu2"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := n.Compare(
				Response{Status: 200, Body: []byte(tt.reference)},
				Response{Status: 200, Body: []byte(tt.candidate)},
			)
			if len(diffs) != 0 {
				t.Errorf("diffs = %v, want none", diffs)
			}
		})
	}
}

// TestTrashSuffixDoesNotEatOrdinaryNames guards the anchor. A pattern loose enough to rewrite
// a real filename would hide a genuine difference in what a service named a file.
func TestTrashSuffixDoesNotEatOrdinaryNames(t *testing.T) {
	n := NewNormalizer("run1")

	diffs := n.Compare(
		Response{Status: 200, Body: []byte(`{"p":"/cyverse/home/wregglej/report.abcdefg"}`)},
		Response{Status: 200, Body: []byte(`{"p":"/cyverse/home/wregglej/report.zyxwvut"}`)},
	)
	if len(diffs) == 0 {
		t.Error("two different filenames outside the trash compared equal")
	}
}

// TestValidateRunID guards the substitution from damaging what it runs over.
//
// A run called "c1" rewrote the uuid cc1b6e6c-... into cc{RUN}b6e6c-..., which then no
// longer matched the uuid pattern and came back as a difference on every case that returned
// one. Nine cases in a group of thirty-three, all of them spurious.
func TestValidateRunID(t *testing.T) {
	tests := []struct {
		name    string
		runID   string
		wantErr bool
	}{
		{"the default shape", "20260824T183000", false},
		{"a word and a number", "run1", false},
		{"all hex, short", "c1", true},
		{"all hex, longer", "abcdef", true},
		{"all decimal", "12345", true},
		{"too short to be distinctive", "xy", true},
		{"hex with one letter outside the range", "abcdz", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRunID(tt.runID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRunID(%q) = %v, wantErr %v", tt.runID, err, tt.wantErr)
			}
		})
	}
}

// TestRunIDDoesNotCorruptAUUID is the same guard from the other side: even a run id that
// passes validation must not be substituted into a uuid, which is why the structured values
// are canonicalised first.
func TestRunIDDoesNotCorruptAUUID(t *testing.T) {
	// "ab1" is not all hex -- it has no character outside the range, so validation would
	// reject it; use one that passes but still appears inside the uuid below.
	n := NewNormalizer("run1")

	diffs := n.Compare(
		Response{Status: 200, Body: []byte(`{"id":"run1ce8-9fec-11f1-8a8b-28924acd7818","p":"/x/run1/a"}`)},
		Response{Status: 200, Body: []byte(`{"id":"run1ce8-9fec-11f1-8a8b-28924acd7818","p":"/x/run1/a"}`)},
	)
	if len(diffs) != 0 {
		t.Errorf("diffs = %v, want none", diffs)
	}
}
