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

// TestCompareReportsHeaderAndBodyTogether matters because a download's body is compared as
// bytes on a path that used to return early, and a header difference there must not be lost.
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
