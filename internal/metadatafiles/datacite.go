package metadatafiles

import (
	"slices"
	"strings"
)

// The DataCite schema this service emits.
//
// Kernel 3, not 4. The library the Clojure service calls has a 4.2 implementation as well,
// but the service requires the kernel-3 namespace, and every file already in the data store
// carries it -- so that is what this writes. Changing it is a DataONE-visible decision, not a
// port detail.
const (
	dataciteNamespace  = "http://datacite.org/schema/kernel-3"
	xsiNamespace       = "http://www.w3.org/2001/XMLSchema-instance"
	dataciteSchemaLocs = "https://schema.datacite.org/meta/kernel-3.1 " +
		"https://schema.datacite.org/meta/kernel-3.1/metadata.xsd"
)

// AVU is one metadata triple a document is built from.
type AVU struct {
	Attribute string
	Value     string
}

// MissingAttributesError names the required attributes a data set does not have.
type MissingAttributesError struct{ Attributes []string }

func (e *MissingAttributesError) Error() string {
	return "missing required attributes: " + strings.Join(e.Attributes, ", ")
}

// requiredAttributes are what a DataCite document cannot be built without. The order is the
// one the Clojure service reports them in.
var requiredAttributes = []string{
	"Identifier", "identifierType",
	"datacite.creator", "creatorAffiliation",
	"datacite.title",
	"datacite.publisher",
	"datacite.publicationyear",
	"datacite.resourcetype",
}

// BuildDataCite renders a data set's AVUs as a DataCite document.
//
// The element order is fixed by the schema and reproduced exactly: the six required elements
// in their required order, then the optional ones in the order the Clojure service generates
// them. Reordering would produce a document DataONE rejects.
func BuildDataCite(avus []AVU) (string, error) {
	if missing := missingAttributes(avus); len(missing) > 0 {
		return "", &MissingAttributesError{Attributes: missing}
	}
	for _, a := range associations {
		if err := a.check(avus); err != nil {
			return "", err
		}
	}

	root := Elem("datacite:resource", []Attr{
		{Name: "xmlns:datacite", Value: dataciteNamespace},
		{Name: "xmlns:xsi", Value: xsiNamespace},
		{Name: "xsi:schemaLocation", Value: dataciteSchemaLocs},
	},
		identifier(avus),
		creators(avus),
		titles(avus),
		Text("datacite:publisher", nil, value(avus, "datacite.publisher")),
		Text("datacite:publicationYear", nil, value(avus, "datacite.publicationyear")),
		resourceType(avus),
		subjects(avus),
		contributors(avus),
		alternateIdentifiers(avus),
		relatedIdentifiers(avus),
		rightsList(avus),
		descriptions(avus),
		geoLocations(avus),
	)

	return Render(root), nil
}

// missingAttributes reports which required attributes have no non-blank value.
func missingAttributes(avus []AVU) []string {
	present := map[string]bool{}
	for _, avu := range avus {
		if strings.TrimSpace(avu.Value) != "" {
			present[avu.Attribute] = true
		}
	}

	var missing []string
	for _, name := range requiredAttributes {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func identifier(avus []AVU) *Element {
	// The doi: prefix is stripped. DataCite wants the bare identifier, and the DE records
	// it with the prefix.
	id := strings.TrimPrefix(value(avus, "Identifier"), "doi:")

	return Text("datacite:identifier",
		[]Attr{{Name: "identifierType", Value: value(avus, "identifierType")}}, id)
}

func creators(avus []AVU) *Element {
	rows := creatorAttributes.rows(avus)

	children := make([]*Element, 0, len(rows))
	for _, row := range rows {
		name, affiliation, nameID := row[0], row[1], row[2]

		var identifier *Element
		if strings.TrimSpace(nameID) != "" {
			identifier = Text("datacite:nameIdentifier",
				[]Attr{{Name: "nameIdentifierScheme", Value: "ORCID"}}, nameID)
		}

		children = append(children, Elem("datacite:creator", nil,
			Text("datacite:creatorName", nil, name),
			identifier,
			Text("datacite:affiliation", nil, affiliation),
		))
	}

	return Elem("datacite:creators", nil, children...)
}

func titles(avus []AVU) *Element {
	children := make([]*Element, 0)
	for _, title := range values(avus, "datacite.title") {
		children = append(children, Text("datacite:title", []Attr{{Name: "xml:lang", Value: "en"}}, title))
	}
	return Elem("datacite:titles", nil, children...)
}

func resourceType(avus []AVU) *Element {
	// The general type is always Dataset; the value is the specific one the DE recorded.
	return Text("datacite:resourceType",
		[]Attr{{Name: "resourceTypeGeneral", Value: "Dataset"}}, value(avus, "datacite.resourcetype"))
}

func subjects(avus []AVU) *Element {
	// A single attribute may carry several subjects separated by commas, which the DE's UI
	// writes that way.
	var children []*Element
	for _, raw := range values(avus, "Subject") {
		for _, subject := range splitOnCommas(raw) {
			children = append(children, Text("datacite:subject",
				[]Attr{{Name: "xml:lang", Value: "en"}}, subject))
		}
	}
	if len(children) == 0 {
		return nil
	}
	return Elem("datacite:subjects", nil, children...)
}

func contributors(avus []AVU) *Element {
	rows := contributorAttributes.rows(avus)
	if len(rows) == 0 {
		return nil
	}

	children := make([]*Element, 0, len(rows))
	for _, row := range rows {
		children = append(children, Elem("datacite:contributor",
			[]Attr{{Name: "contributorType", Value: row[1]}},
			Text("datacite:contributorName", nil, row[0])))
	}
	return Elem("datacite:contributors", nil, children...)
}

func alternateIdentifiers(avus []AVU) *Element {
	rows := alternateIdentifierAttributes.rows(avus)
	if len(rows) == 0 {
		return nil
	}

	children := make([]*Element, 0, len(rows))
	for _, row := range rows {
		children = append(children, Text("datacite:alternateIdentifier",
			[]Attr{{Name: "alternateIdentifierType", Value: row[1]}}, row[0]))
	}
	return Elem("datacite:alternateIdentifiers", nil, children...)
}

func relatedIdentifiers(avus []AVU) *Element {
	rows := relatedIdentifierAttributes.rows(avus)
	if len(rows) == 0 {
		return nil
	}

	children := make([]*Element, 0, len(rows))
	for _, row := range rows {
		children = append(children, Text("datacite:relatedIdentifier", []Attr{
			{Name: "relatedIdentifierType", Value: row[1]},
			{Name: "relationType", Value: row[2]},
		}, row[0]))
	}
	return Elem("datacite:relatedIdentifiers", nil, children...)
}

func rightsList(avus []AVU) *Element {
	held := values(avus, "Rights")
	if len(held) == 0 {
		return nil
	}

	children := make([]*Element, 0, len(held))
	for _, rights := range held {
		children = append(children, Text("datacite:rights", nil, rights))
	}
	return Elem("datacite:rightsList", nil, children...)
}

func descriptions(avus []AVU) *Element {
	rows := descriptionAttributes.rows(avus)
	if len(rows) == 0 {
		return nil
	}

	children := make([]*Element, 0, len(rows))
	for _, row := range rows {
		children = append(children, Text("datacite:description", []Attr{
			{Name: "descriptionType", Value: row[1]},
			{Name: "xml:lang", Value: "en"},
		}, row[0]))
	}
	return Elem("datacite:descriptions", nil, children...)
}

func geoLocations(avus []AVU) *Element {
	// All three are optional, so a location is built from whichever of them a row has.
	rows := geoLocationAttributes.rows(avus)
	if len(rows) == 0 {
		return nil
	}

	children := make([]*Element, 0, len(rows))
	for _, row := range rows {
		children = append(children, Elem("datacite:geoLocation", nil,
			optionalText("datacite:geoLocationPoint", row[0]),
			optionalText("datacite:geoLocationBox", row[1]),
			optionalText("datacite:geoLocationPlace", row[2]),
		))
	}
	return Elem("datacite:geoLocations", nil, children...)
}

// optionalText builds an element only when there is something to put in it.
func optionalText(name, text string) *Element {
	if text == "" {
		return nil
	}
	return Text(name, nil, text)
}

// values returns the trimmed values recorded under an attribute, in the order they appear.
func values(avus []AVU, attribute string) []string {
	var out []string
	for _, avu := range avus {
		if avu.Attribute == attribute {
			out = append(out, strings.TrimSpace(avu.Value))
		}
	}
	return out
}

// value returns the first value recorded under an attribute.
func value(avus []AVU, attribute string) string {
	if held := values(avus, attribute); len(held) > 0 {
		return held[0]
	}
	return ""
}

// association is a set of attributes whose values the DE pairs by position: the first
// creator's name goes with the first affiliation, the second with the second, and so on.
type association struct {
	required []string
	optional []string
}

var (
	creatorAttributes = association{
		required: []string{"datacite.creator", "creatorAffiliation"},
		optional: []string{"creatorNameIdentifier"},
	}
	contributorAttributes         = association{required: []string{"contributorName", "contributorType"}}
	alternateIdentifierAttributes = association{required: []string{"AlternateIdentifier", "alternateIdentifierType"}}
	relatedIdentifierAttributes   = association{
		required: []string{"RelatedIdentifier", "relatedIdentifierType", "relationType"},
	}
	descriptionAttributes = association{required: []string{"Description", "descriptionType"}}
	geoLocationAttributes = association{optional: []string{"geoLocationPoint", "geoLocationBox", "geoLocationPlace"}}
)

// associations is every association a document uses, all checked before any is built.
var associations = []association{
	creatorAttributes,
	contributorAttributes,
	alternateIdentifierAttributes,
	relatedIdentifierAttributes,
	descriptionAttributes,
	geoLocationAttributes,
}

// AssociationError reports attributes whose values cannot be paired by position.
type AssociationError struct{ Reason string }

func (e *AssociationError) Error() string { return e.Reason }

// check refuses values that cannot be paired: required attributes with different numbers of
// values, or an optional one with more than they have. Pairing them anyway would silently
// drop or misattribute values in a published record.
func (a association) check(avus []AVU) error {
	if len(a.required) == 0 {
		return nil
	}

	count := len(values(avus, a.required[0]))
	for _, name := range a.required[1:] {
		if len(values(avus, name)) != count {
			return &AssociationError{Reason: "these attributes must have the same number of values: " +
				strings.Join(a.required, ", ")}
		}
	}
	for _, name := range a.optional {
		if len(values(avus, name)) > count {
			return &AssociationError{Reason: "none of the attributes " + strings.Join(a.optional, ", ") +
				" may have more values than " + strings.Join(a.required, ", ")}
		}
	}
	return nil
}

// rows zips the association's values together, padding optional attributes with blanks. It
// assumes check has passed.
func (a association) rows(avus []AVU) [][]string {
	names := slices.Concat(a.required, a.optional)
	lists := make([][]string, 0, len(names))
	longest := 0
	for _, name := range names {
		held := values(avus, name)
		lists = append(lists, held)
		longest = max(longest, len(held))
	}

	out := make([][]string, 0, longest)
	for i := range longest {
		row := make([]string, 0, len(lists))
		for _, held := range lists {
			if i < len(held) {
				row = append(row, held[i])
				continue
			}
			row = append(row, "")
		}
		out = append(out, row)
	}

	// Leading rows that are entirely blank are dropped, which is what the Clojure service
	// does.
	for len(out) > 0 && allBlank(out[0]) {
		out = out[1:]
	}
	return out
}

func allBlank(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

// splitOnCommas splits a value on commas, ignoring the space around them.
func splitOnCommas(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
