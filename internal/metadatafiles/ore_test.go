package metadatafiles

import "testing"

// The golden was produced by org.cyverse/oai-ore 1.0.4, the library the deployed service
// calls. DataONE reads these files to decide what to harvest, so the comparison is byte for
// byte -- including the order the namespaces are declared in, which is not meaningful to a
// parser and is meaningful to a diff.
func TestBuildOREMatchesTheReference(t *testing.T) {
	const base = "https://mn.example.org/mn/v1/object/"

	file := func(id string) ArchivedFile { return ArchivedFile{ID: id, URI: base + id} }

	got := BuildORE(OREInput{
		AggregationURI: base + "aaaaaaaa-0000-0000-0000-000000000001",
		ResourceMap:    file("bbbbbbbb-0000-0000-0000-000000000002"),
		Metadata:       file("eeeeeeee-0000-0000-0000-000000000005"),
		Files: []ArchivedFile{
			file("cccccccc-0000-0000-0000-000000000003"),
			file("dddddddd-0000-0000-0000-000000000004"),
		},
		AVUs: []AVU{
			{Attribute: "datacite.title", Value: "A Test Data Set"},
			{Attribute: "datacite.creator", Value: "Doe, Jane"},
			{Attribute: "Identifier", Value: "doi:10.5072/FK2ABC123"},
		},
	})

	want := golden(t, "ore.xml")
	if got != want {
		t.Errorf("the resource map differs from the reference's.\n got: %s\nwant: %s\nfirst difference at byte %d",
			got, want, firstDifference(got, want))
	}
}

// Only a handful of attributes appear in a resource map; the rest of a data set's metadata is
// carried by the DataCite file beside it. An attribute this does not know must contribute
// nothing rather than an element DataONE would not expect.
func TestBuildOREIgnoresAttributesItDoesNotCarry(t *testing.T) {
	const base = "https://mn.example.org/mn/v1/object/"

	got := BuildORE(OREInput{
		AggregationURI: base + "agg",
		ResourceMap:    ArchivedFile{ID: "map", URI: base + "map"},
		AVUs: []AVU{
			{Attribute: "datacite.title", Value: "Kept"},
			{Attribute: "ipc-trash-origin", Value: "/z/home/u/a"},
			{Attribute: "something-nobody-declared", Value: "dropped"},
			{Attribute: "datacite.publisher", Value: "   "},
		},
	})

	if !contains([]string{got}, "<dc:title>Kept</dc:title>") {
		t.Errorf("the title is missing: %s", got)
	}
	for _, unwanted := range []string{"trash-origin", "dropped", "dc:publisher"} {
		if contains([]string{got}, unwanted) {
			t.Errorf("%q appears in the resource map: %s", unwanted, got)
		}
	}
}
