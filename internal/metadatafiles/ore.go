package metadatafiles

import "strings"

// OREFormatID identifies the format of the resource map, recorded as an AVU on the file so
// that DataONE knows what it is.
const OREFormatID = "http://www.openarchives.org/ore/terms"

// oreNamespaces are declared on the root element in exactly this order.
//
// The order is not meaningful to a parser and is meaningful to a byte comparison. It is the
// order the Clojure service emits, which comes from Clojure's hash-map iteration -- stable
// for a given set of keys, and verified stable across runs, but not something to derive. It
// is written out here instead.
var oreNamespaces = []Attr{
	{Name: "xmlns:dc", Value: "http://purl.org/dc/elements/1.1/"},
	{Name: "xmlns:owl", Value: "http://www.w3.org/2002/07/owl#"},
	{Name: "xmlns:foaf", Value: "http://xmlns.com/foaf/0.1/"},
	{Name: "xmlns:dcterms", Value: "http://purl.org/dc/terms/"},
	{Name: "xmlns:rdf", Value: "http://www.w3.org/1999/02/22-rdf-syntax-ns#"},
	{Name: "xmlns:ore", Value: "http://www.openarchives.org/ore/terms/"},
	{Name: "xmlns:rdfs", Value: "http://www.w3.org/2000/01/rdf-schema#"},
	{Name: "xmlns:cito", Value: "http://purl.org/spar/cito/"},
}

// The RDF types a resource map names.
const (
	aggregationType = "http://www.openarchives.org/ore/terms/Aggregation"
	resourceMapType = "http://www.openarchives.org/ore/terms/ResourceMap"
)

// ArchivedFile is one object in a data set, as the resource map refers to it.
type ArchivedFile struct {
	// ID is the object's UUID, and URI is where the member node serves it.
	ID  string
	URI string
}

// OREInput is everything a resource map is built from.
type OREInput struct {
	// AggregationURI names the data set as a whole.
	AggregationURI string

	// ResourceMap is the file being written: a resource map describes itself.
	ResourceMap ArchivedFile

	// Metadata is the DataCite file that documents the data set's contents.
	Metadata ArchivedFile

	// Files are the data set's own objects, without the two files above.
	Files []ArchivedFile

	// AVUs describe the data set. Only the handful below appear; the rest are ignored.
	AVUs []AVU
}

// dublinCoreElement maps a DataCite attribute onto the element that carries it in a resource
// map. An attribute absent from this table contributes nothing, which is most of them.
var dublinCoreElement = map[string]string{
	"datacite.title":        "dc:title",
	"datacite.publisher":    "dc:publisher",
	"datacite.creator":      "dc:creator",
	"datacite.resourcetype": "dc:type",
	"contributorName":       "dc:contributor",
	"Subject":               "dc:subject",
	"Rights":                "dc:rights",
	"Description":           "dc:description",
	"Identifier":            "dcterms:identifier",
}

// dcTermsElement maps the geographic attributes, which carry a parse type the others do not.
var dcTermsElement = map[string]string{
	"geoLocationBox":   "dcterms:Box",
	"geoLocationPlace": "dcterms:Location",
	"geoLocationPoint": "dcterms:Point",
}

// BuildORE renders a data set's resource map.
//
// A resource map says what a data set contains, which file describes it, and what the data
// set is called. DataONE reads it to decide what to harvest, so the element order, the
// namespace prefixes and the order of the descriptions are all reproduced from the Clojure
// service rather than chosen.
func BuildORE(in OREInput) string {
	descriptions := []*Element{
		aggregation(in),
		resourceMap(in),
	}

	if in.Metadata.URI != "" {
		descriptions = append(descriptions, metadataFile(in))
	}
	for _, file := range in.Files {
		descriptions = append(descriptions, archivedFile(file, in.Metadata.URI))
	}

	return Render(Elem("rdf:RDF", oreNamespaces, descriptions...))
}

// aggregation describes the data set itself: what it holds and what it is called.
func aggregation(in OREInput) *Element {
	children := []*Element{
		Elem("rdf:type", []Attr{{Name: "rdf:resource", Value: aggregationType}}),
	}

	// The metadata file is listed first, ahead of the data set's own objects.
	for _, uri := range append([]string{in.Metadata.URI}, urisOf(in.Files)...) {
		if uri == "" {
			continue
		}
		children = append(children, Elem("ore:aggregates", []Attr{{Name: "rdf:resource", Value: uri}}))
	}

	for _, avu := range in.AVUs {
		if element := describeAVU(avu); element != nil {
			children = append(children, element)
		}
	}

	return Elem("rdf:Description", []Attr{{Name: "rdf:about", Value: in.AggregationURI}}, children...)
}

// resourceMap describes the file being written, which points back at the data set.
func resourceMap(in OREInput) *Element {
	return Elem("rdf:Description", []Attr{{Name: "rdf:about", Value: in.ResourceMap.URI}},
		Text("dcterms:identifier", nil, in.ResourceMap.ID),
		Elem("rdf:type", []Attr{{Name: "rdf:resource", Value: resourceMapType}}),
		Elem("ore:describes", []Attr{{Name: "rdf:resource", Value: in.AggregationURI}}),
	)
}

// metadataFile describes the DataCite file and what it documents.
func metadataFile(in OREInput) *Element {
	children := []*Element{Text("dcterms:identifier", nil, in.Metadata.ID)}

	for _, file := range in.Files {
		children = append(children,
			Elem("cito:documents", []Attr{{Name: "rdf:resource", Value: file.URI}}))
	}

	return Elem("rdf:Description", []Attr{{Name: "rdf:about", Value: in.Metadata.URI}}, children...)
}

// archivedFile describes one of the data set's objects.
func archivedFile(file ArchivedFile, metadataURI string) *Element {
	children := []*Element{Text("dcterms:identifier", nil, file.ID)}

	if metadataURI != "" {
		children = append(children,
			Elem("cito:isDocumentedBy", []Attr{{Name: "rdf:resource", Value: metadataURI}}))
	}

	return Elem("rdf:Description", []Attr{{Name: "rdf:about", Value: file.URI}}, children...)
}

// describeAVU renders one AVU, or nothing when it is not one a resource map carries.
func describeAVU(avu AVU) *Element {
	value := strings.TrimSpace(avu.Value)
	if value == "" {
		return nil
	}

	if name, ok := dublinCoreElement[avu.Attribute]; ok {
		return Text(name, nil, value)
	}
	if name, ok := dcTermsElement[avu.Attribute]; ok {
		return Text(name, []Attr{{Name: "rdf:parseType", Value: "Literal"}}, value)
	}
	return nil
}

// urisOf lists the addresses of a set of objects.
func urisOf(files []ArchivedFile) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.URI)
	}
	return out
}
