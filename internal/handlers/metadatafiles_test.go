package handlers

import (
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/service"
)

// The delimiter arrives URL-encoded, because a comma cannot be sent in a query parameter
// unescaped.
func TestCSVSeparator(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    rune
		wantErr bool
	}{
		{name: "absent means a comma", in: "", want: ','},
		{name: "an encoded comma", in: "%2C", want: ','},
		{name: "a literal tab", in: "\t", want: '\t'},
		{name: "an encoded tab", in: "%09", want: '\t'},
		{name: "a semicolon", in: ";", want: ';'},
		{name: "more than one character", in: "ab", wantErr: true},
		{name: "an encoded string", in: "%2C%2C", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := csvSeparator(tc.in)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("csvSeparator(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("csvSeparator(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("csvSeparator(%q) = %q, want %q", tc.in, got, tc.want)
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
// fewer AVUs. A blank attribute name contributes nothing.
func TestCSVAVUs(t *testing.T) {
	attributes := []string{"item", "", "institution", "department"}

	got := csvAVUs(attributes, []string{"fake-1", "ignored", "UofA"})

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
