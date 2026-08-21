// Package metadatafiles builds the XML documents this service writes into the data store.
//
// These files leave the DE: DataONE reads them, and it is sensitive to namespace prefixes,
// attribute order and escaping. So the documents are emitted a token at a time by the writer
// here rather than marshalled from structs.
//
// encoding/xml cannot be used for this. It reorders attributes, it chooses its own namespace
// prefixes, and it escapes more characters than the reference does -- a quotation mark in a
// title would come out as &#34; where the deployed service emits it literally. The escaping
// rules below were measured against clojure.data.xml, which is what wrote every one of these
// files that already exists.
package metadatafiles

import (
	"io"
	"strings"
)

// Attr is one attribute. They are a slice rather than a map because their order is part of
// the output.
type Attr struct {
	Name  string
	Value string
}

// Element is one node. An element has either text or children, never both, which is all these
// documents need.
type Element struct {
	Name     string
	Attrs    []Attr
	Text     string
	Children []*Element

	// hasText separates an element holding the empty string from one holding nothing. The
	// two are written differently -- <x></x> against <x/> -- and the field exists because
	// an empty Text alone cannot tell them apart.
	hasText bool
}

// Elem builds an element with children.
func Elem(name string, attrs []Attr, children ...*Element) *Element {
	kept := make([]*Element, 0, len(children))
	for _, child := range children {
		// Absent children are passed as nil so that a caller can write the optional parts
		// of a document as a flat list rather than as a chain of conditionals.
		if child != nil {
			kept = append(kept, child)
		}
	}
	return &Element{Name: name, Attrs: attrs, Children: kept}
}

// Text builds an element containing text.
//
// Text always writes an open and a close tag, even for the empty string. Only an element
// given no content at all is self-closed, which is the distinction the reference draws.
func Text(name string, attrs []Attr, text string) *Element {
	return &Element{Name: name, Attrs: attrs, Text: text, hasText: true}
}

// declaration is the prologue every one of these documents opens with, byte for byte.
const declaration = `<?xml version="1.0" encoding="UTF-8"?>`

// Write renders a document: the declaration and then the element, with no trailing newline
// and no indentation.
func Write(w io.Writer, root *Element) error {
	out := &writer{w: w}

	out.write(declaration)
	out.element(root)
	return out.err
}

// Render returns a document as a string.
//
// There is no error to return: the only thing Write can fail on is the writer, and a
// strings.Builder does not fail.
func Render(root *Element) string {
	var out strings.Builder

	writer := &writer{w: &out}
	writer.write(declaration)
	writer.element(root)

	return out.String()
}

// writer accumulates output, remembering the first failure so that the caller checks once
// rather than after every token.
type writer struct {
	w   io.Writer
	err error
}

func (x *writer) write(s string) {
	if x.err != nil {
		return
	}
	_, x.err = io.WriteString(x.w, s)
}

func (x *writer) element(e *Element) {
	x.write("<" + e.Name)

	for _, attr := range e.Attrs {
		x.write(" " + attr.Name + `="` + escapeAttr(attr.Value) + `"`)
	}

	// An element given no content at all is self-closed; one given the empty string is not.
	// The reference draws exactly that line, and these documents are compared byte for
	// byte, so it is drawn here too.
	if !e.hasText && len(e.Children) == 0 {
		x.write("/>")
		return
	}

	x.write(">")

	if e.hasText {
		x.write(escapeText(e.Text))
	}
	for _, child := range e.Children {
		x.element(child)
	}

	x.write("</" + e.Name + ">")
}

// textEscapes are what the reference escapes in element content.
//
// Notably absent: the quotation mark, the apostrophe, and the whitespace characters. Go's own
// encoder escapes all of those, which is why it cannot be used here.
var textEscapes = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

// attrEscapes are what the reference escapes in an attribute value.
//
// The quotation mark is escaped here and not in text, because it would otherwise close the
// value. A literal newline or tab in an attribute value is left alone, which an XML parser
// will normalise to a space on the way back in -- that is the reference's behaviour and these
// files are compared as bytes, so it is reproduced rather than corrected.
var attrEscapes = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
)

func escapeText(s string) string { return textEscapes.Replace(s) }
func escapeAttr(s string) string { return attrEscapes.Replace(s) }
