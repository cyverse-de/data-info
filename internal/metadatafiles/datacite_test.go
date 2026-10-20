package metadatafiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// golden reads a testdata file. The goldens came from org.cyverse/metadata-files 2.1.2, which
// wrote the existing documents, so comparisons are byte for byte.
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

func TestBuildDataCiteMatchesTheGolden(t *testing.T) {
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
			// encoding/xml would escape the quotes, apostrophes, tabs and newlines here.
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
				t.Errorf("the document differs from the golden file.\n got: %s\nwant: %s\nfirst difference at byte %d",
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

// An attribute with a blank value counts as missing.
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

func TestBuildDataCiteRefusesUnpairableValues(t *testing.T) {
	cases := []struct {
		name    string
		extra   []AVU
		wantErr bool
	}{
		{
			name:    "a creator with no affiliation",
			extra:   []AVU{avu("datacite.creator", "Roe, Rich")},
			wantErr: true,
		},
		{
			name:    "more name identifiers than creators",
			extra:   []AVU{avu("creatorNameIdentifier", "0000-0001"), avu("creatorNameIdentifier", "0000-0002")},
			wantErr: true,
		},
		{
			name:    "a related identifier missing its relation type",
			extra:   []AVU{avu("RelatedIdentifier", "x"), avu("relatedIdentifierType", "DOI")},
			wantErr: true,
		},
		{
			name:  "fewer name identifiers than creators",
			extra: []AVU{avu("datacite.creator", "Roe, Rich"), avu("creatorAffiliation", "UA")},
		},
		{
			name:  "optional-only attributes of different lengths",
			extra: []AVU{avu("geoLocationPoint", "1 2"), avu("geoLocationPoint", "3 4"), avu("geoLocationPlace", "Tucson")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildDataCite(append(minimalAVUs(), tc.extra...))

			var association *AssociationError
			if got := errors.As(err, &association); got != tc.wantErr {
				t.Errorf("BuildDataCite returned %v, want an *AssociationError: %v", err, tc.wantErr)
			}
		})
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
