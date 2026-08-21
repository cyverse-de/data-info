package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// The delimiter is sent URL-encoded, because a comma cannot go in a query parameter
// unescaped -- but echo decodes it before a handler sees it. The cases here go through a real
// request for that reason: testing the helper against the still-encoded value hid a second
// decode that rejected a literal percent sign.
func TestCSVSeparator(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		want    rune
		wantErr bool
	}{
		{name: "absent means a comma", query: "", want: ','},
		{name: "an encoded comma", query: "separator=%2C", want: ','},
		{name: "an encoded tab", query: "separator=%09", want: '\t'},
		// Encoded, because Go's query parser drops a parameter list containing a bare
		// semicolon. The endpoint documents the value as URL-encoded for this reason.
		{name: "an encoded semicolon", query: "separator=%3B", want: ';'},
		{name: "an encoded percent sign", query: "separator=%25", want: '%'},
		{name: "more than one character", query: "separator=ab", wantErr: true},
		{name: "an encoded string", query: "separator=%2C%2C", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/x?"+tc.query, nil)
			c := e.NewContext(req, httptest.NewRecorder())

			got, err := csvSeparator(c.QueryParam("separator"))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("separator %q = %q, want an error", tc.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("separator %q: %v", tc.query, err)
			}
			if got != tc.want {
				t.Errorf("separator %q = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

// A path that is not absolute is taken as relative to the item named in the URL, which is
// what lets one file describe a whole folder's contents.
func TestResolveCSVPath(t *testing.T) {
	const base = "/iplant/home/wregglej/target"

	cases := []struct {
		in   string
		want string
	}{
		{"library1/fake.fastq.gz", base + "/library1/fake.fastq.gz"},
		{"/iplant/home/other/absolute.txt", "/iplant/home/other/absolute.txt"},
		{"trailing/", base + "/trailing"},
		{"library1", base + "/library1"},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := resolveCSVPath(base, tc.in); got != tc.want {
				t.Errorf("resolveCSVPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A row with fewer values than the header has attributes is not an error; it simply carries
// fewer AVUs. The result is always a slice and never nil, because the response declares an
// array and a nil one marshals to null.
func TestCSVAVUs(t *testing.T) {
	attributes := []string{"item", "institution", "department"}

	got := csvAVUs(attributes, []string{"fake-1", "UofA"})

	want := []service.AVU{
		{Attribute: "item", Value: "fake-1"},
		{Attribute: "institution", Value: "UofA"},
	}
	if len(got) != len(want) {
		t.Fatalf("avus = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("avus[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	if empty := csvAVUs(nil, nil); empty == nil {
		t.Error("an empty result is nil, which marshals to null rather than []")
	}
}

// The metadata files go beside the data set's parent rather than inside the data set, so that
// harvesting the data set does not harvest its own metadata.
func TestMetadataDirFor(t *testing.T) {
	deps, _ := testDeps(t)
	deps.DataONE = DataONESettings{MetadataDirname: "curated_metadata"}
	avus := NewAVUs(deps)

	got := avus.metadataDirFor("/iplant/home/wregglej/repo/curated/dataset")

	if want := "/iplant/home/wregglej/repo/curated_metadata/dataset"; got != want {
		t.Errorf("metadataDirFor = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "/iplant/home/wregglej/repo/curated/") {
		t.Error("the metadata directory is inside the data set, so harvesting would pick it up")
	}
}

// The metadata service's response is passed through untouched except for the AVU list, which
// is merged with what iRODS holds.
func TestTemplateAVUs(t *testing.T) {
	got := templateAVUs(map[string]any{
		"avus": []any{
			map[string]any{"attr": "datacite.title", "value": "A Data Set", "unit": ""},
			map[string]any{"attr": "datacite.creator", "value": "Doe, Jane"},
			"not an object",
		},
		"something-else": "ignored",
	})

	if len(got) != 2 {
		t.Fatalf("avus = %+v, want the two that are objects", got)
	}
	if got[0].Attribute != "datacite.title" || got[0].Value != "A Data Set" {
		t.Errorf("avus[0] = %+v", got[0])
	}
}
