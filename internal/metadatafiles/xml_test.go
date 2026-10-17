package metadatafiles

import "testing"

// TestEmptyElementsMatchTheGoldens pins when an element self-closes. Both goldens here came
// from clojure.data.xml, which wrote the existing documents.
func TestEmptyElementsMatchTheGoldens(t *testing.T) {
	// No content self-closes; an empty string does not.
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

// encoding/xml escapes quotes, apostrophes, tabs and newlines that the existing files leave
// literal.
func TestEscapingMatchesTheGoldens(t *testing.T) {
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

func TestNilChildrenAreDropped(t *testing.T) {
	got := Render(Elem("x", nil, nil, Text("kept", nil, "yes"), nil))

	if want := `<?xml version="1.0" encoding="UTF-8"?><x><kept>yes</kept></x>`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
