package metadatafiles

import "testing"

// Both golden files here were produced by clojure.data.xml, which wrote every one of these
// documents that already exists in the data store. They pin the two things a Go port gets
// wrong by default: which characters are escaped, and when an element is self-closed.
func TestEmptyElementsMatchTheReference(t *testing.T) {
	// An element given no content is self-closed; one given the empty string is not. Getting
	// this backwards produced a resource map that differed from the reference on every
	// rdf:type element.
	got := Render(Elem("root", nil,
		Elem("nochildren", []Attr{{Name: "a", Value: "1"}}),
		Text("emptystring", nil, ""),
		Elem("nilcontent", nil),
		Text("space", nil, " "),
	))

	want := golden(t, "empties.xml")
	if got != want {
		t.Errorf("got:  %s\nwant: %s\nfirst difference at byte %d", got, want, firstDifference(got, want))
	}
}

// The standard library escapes quotation marks, apostrophes, tabs and newlines that the
// reference leaves alone, which is why encoding/xml cannot write these documents.
func TestEscapingMatchesTheReference(t *testing.T) {
	const tricky = "amp& lt< gt> quote\" apos' tab\there nl\nthere cr\rthere"

	got := Render(Elem("root", []Attr{{Name: "a", Value: tricky}},
		Text("child", nil, tricky),
	))

	want := golden(t, "escaping.xml")
	if got != want {
		t.Errorf("got:  %s\nwant: %s\nfirst difference at byte %d", got, want, firstDifference(got, want))
	}
}

// Attribute order is part of the output, so it comes from a slice rather than a map.
func TestAttributeOrderIsPreserved(t *testing.T) {
	got := Render(Elem("x", []Attr{
		{Name: "z", Value: "1"},
		{Name: "a", Value: "2"},
		{Name: "m", Value: "3"},
	}))

	if want := `<?xml version="1.0" encoding="UTF-8"?><x z="1" a="2" m="3"/>`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// Absent children are passed as nil so a document's optional parts can be written as a flat
// list rather than a chain of conditionals.
func TestNilChildrenAreDropped(t *testing.T) {
	got := Render(Elem("x", nil, nil, Text("kept", nil, "yes"), nil))

	if want := `<?xml version="1.0" encoding="UTF-8"?><x><kept>yes</kept></x>`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
