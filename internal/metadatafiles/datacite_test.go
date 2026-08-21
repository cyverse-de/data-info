package metadatafiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden files in testdata were produced by the reference implementation itself --
// org.cyverse/metadata-files 2.1.2, the library the deployed service calls -- rather than
// written by hand. These documents go into the data store and DataONE reads them, so the
// comparison is byte for byte: a different namespace prefix, a different attribute order or
// a differently escaped quotation mark is a different document.
func golden(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading the golden file: %v", err)
	}
	return string(raw)
}

func avu(attribute, value string) AVU { return AVU{Attribute: attribute, Value: value} }

// minimalAVUs carries exactly the required attributes and nothing else.
func minimalAVUs() []AVU {
	return []AVU{
		avu("Identifier", "doi:10.5072/FK2ABC123"),
		avu("identifierType", "DOI"),
		avu("datacite.creator", "Doe, Jane"),
		avu("creatorAffiliation", "CyVerse"),
		avu("datacite.title", "A Test Data Set"),
		avu("datacite.publisher", "CyVerse Data Commons"),
		avu("datacite.publicationyear", "2026"),
		avu("datacite.resourcetype", "Dataset"),
	}
}

func TestBuildDataCiteMatchesTheReference(t *testing.T) {
	cases := []struct {
		name   string
		avus   []AVU
		golden string
	}{
		{name: "only the required attributes", avus: minimalAVUs(), golden: "datacite-minimal.xml"},
		{
			name: "every optional element as well",
			avus: append(minimalAVUs(),
				avu("creatorNameIdentifier", "0000-0002-1825-0097"),
				avu("Subject", "genomics, plants"),
				avu("contributorName", "Roe, Richard"),
				avu("contributorType", "DataCurator"),
				avu("AlternateIdentifier", "ABC-123"),
				avu("alternateIdentifierType", "Local"),
				avu("RelatedIdentifier", "10.5072/FK2XYZ"),
				avu("relatedIdentifierType", "DOI"),
				avu("relationType", "IsSupplementTo"),
				avu("Rights", "CC0 1.0 Universal"),
				avu("Description", "A data set used to pin the XML output."),
				avu("descriptionType", "Abstract"),
				avu("geoLocationPlace", "Tucson, Arizona"),
			),
			golden: "datacite-full.xml",
		},
		{
			// The escaping is where a Go port most easily diverges: the standard library's
			// encoder escapes quotation marks, apostrophes, tabs and newlines that the
			// reference leaves alone.
			name: "characters that need escaping",
			avus: []AVU{
				avu("Identifier", "10.5072/FK2<&>\"'"),
				avu("identifierType", "DO<I>"),
				avu("datacite.creator", "O'Brien, Séan & Co <lab>"),
				avu("creatorAffiliation", "Tab\there\nNewline"),
				avu("datacite.title", "Title with éè and 中文"),
				avu("datacite.publisher", `Pub "quoted"`),
				avu("datacite.publicationyear", "2026"),
				// A non-breaking space, which is how this value ended up in the golden and
				// is worth keeping: it proves multi-byte UTF-8 passes through untouched
				// rather than being escaped to a character reference.
				avu("datacite.resourcetype", "Data\u00a0set"),
				avu("Description", "line1\nline2\ttabbed"),
				avu("descriptionType", "Abstract"),
			},
			golden: "datacite-tricky.xml",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildDataCite(tc.avus)
			if err != nil {
				t.Fatalf("BuildDataCite: %v", err)
			}

			want := golden(t, tc.golden)
			if got != want {
				t.Errorf("the document differs from the reference's.\n got: %s\nwant: %s\nfirst difference at byte %d",
					got, want, firstDifference(got, want))
			}
		})
	}
}

// firstDifference reports where two documents diverge, which is far more useful than being
// shown two long single-line documents.
func firstDifference(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// A data set without the required attributes has no valid document, and the caller is told
// which ones are missing rather than being handed something DataONE would reject.
func TestBuildDataCiteReportsMissingAttributes(t *testing.T) {
	full := minimalAVUs()

	for _, drop := range []string{"Identifier", "datacite.title", "datacite.publicationyear"} {
		t.Run("without "+drop, func(t *testing.T) {
			var kept []AVU
			for _, held := range full {
				if held.Attribute != drop {
					kept = append(kept, held)
				}
			}

			_, err := BuildDataCite(kept)

			var missing *MissingAttributesError
			if !errors.As(err, &missing) {
				t.Fatalf("BuildDataCite returned %v, want a *MissingAttributesError", err)
			}
			if !contains(missing.Attributes, drop) {
				t.Errorf("missing = %v, want it to name %q", missing.Attributes, drop)
			}
		})
	}
}

// A blank value is not a value: an attribute recorded with an empty string is as good as
// absent, which is how the reference decides what is missing.
func TestABlankRequiredValueCountsAsMissing(t *testing.T) {
	avus := minimalAVUs()
	for i := range avus {
		if avus[i].Attribute == "datacite.publisher" {
			avus[i].Value = "   "
		}
	}

	_, err := BuildDataCite(avus)

	var missing *MissingAttributesError
	if !errors.As(err, &missing) {
		t.Fatalf("BuildDataCite returned %v, want a *MissingAttributesError", err)
	}
	if !contains(missing.Attributes, "datacite.publisher") {
		t.Errorf("missing = %v, want it to name the blank attribute", missing.Attributes)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return strings.Contains(strings.Join(values, ","), want)
}
