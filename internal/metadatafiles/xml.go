// Package metadatafiles writes the DataCite and ORE XML documents DataONE harvests. It emits
// tokens directly because encoding/xml reorders attributes, picks its own namespace prefixes,
// and escapes characters that the existing files carry literally.
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

	// hasText distinguishes the empty string (<x></x>) from no content (<x/>).
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

// Text builds an element containing text. It always writes open and close tags, even for the
// empty string.
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

// textEscapes leaves quotes, apostrophes and whitespace literal, as the existing files have
// them.
var textEscapes = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

// attrEscapes adds the quotation mark, which would otherwise close the value. Newlines and tabs
// stay literal to match the existing files, though a parser reads them back as spaces.
var attrEscapes = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
)

func escapeText(s string) string { return textEscapes.Replace(s) }
func escapeAttr(s string) string { return attrEscapes.Replace(s) }
