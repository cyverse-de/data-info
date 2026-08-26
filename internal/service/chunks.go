package service

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The tabular paging arithmetic, kept here rather than in the handler because it is where
// the endpoint's surprises live and it is worth testing without an iRODS server.
//
// It is a faithful port of page_tabular.clj, oddities included. Two are worth naming
// because they look like defects and are not this port's to fix (docs/deferred-fixes.md):
// the page bound is inclusive, so a request one page past the end is accepted and answers
// with whatever the read finds; and ERR_INVALID_PAGE reports the zero-based page number
// while the response reports the one-based one.

// DecodeChunk decodes a chunk of a file as UTF-8, dropping the partial characters at its
// edges.
//
// A chunk is read at an arbitrary byte offset, so it may begin or end in the middle of a
// multi-byte character. Those bytes are dropped rather than decoded, which is what
// clj-jargon's read-at-position does: the alternative is a U+FFFD replacement character at
// each edge, which then appears in the CSV as a column value.
//
// trimLeading is false only when the read started at the beginning of the file, where the
// first byte cannot be a continuation of anything.
func DecodeChunk(buffer []byte, trimLeading bool) string {
	if len(buffer) == 0 {
		return ""
	}

	start := 0
	if trimLeading {
		// At most three, because that is the longest a UTF-8 sequence can trail its lead
		// byte by.
		for start < len(buffer) && start < 3 && isContinuationByte(buffer[start]) {
			start++
		}
	}

	end := len(buffer)
	last := len(buffer) - 1
	for last > start && isContinuationByte(buffer[last]) {
		last--
	}
	if last >= start {
		if need, ok := utf8SequenceLength(buffer[last]); ok && last+need > len(buffer) {
			end = last
		}
	}

	if start >= end {
		return ""
	}
	return string(buffer[start:end])
}

// isContinuationByte reports whether b continues a multi-byte UTF-8 sequence.
func isContinuationByte(b byte) bool { return b&0xC0 == 0x80 }

// utf8SequenceLength is how many bytes the sequence starting at b occupies, and whether b
// can start one at all.
func utf8SequenceLength(b byte) (int, bool) {
	switch {
	case b < 0x80:
		return 1, true
	case b < 0xC0:
		return 0, false
	case b < 0xE0:
		return 2, true
	case b < 0xF0:
		return 3, true
	case b < 0xF8:
		return 4, true
	default:
		return 0, false
	}
}

// TabularPage is where a page of a tabular file starts and how much has to be read to cover
// it.
type TabularPage struct {
	// Page is the zero-based page the caller asked for. The request and the response both
	// use one-based numbering; this is the only place the other one appears.
	Page int64

	// Pages is how many pages the file divides into at the requested size.
	Pages int64

	// LoadOffset and LoadLength are the read that covers the page. Beyond the first page
	// the read starts one page early and is twice as long, so that the page's first whole
	// line can be found by scanning backwards from where the page nominally begins.
	LoadOffset int64
	LoadLength int64
}

// PlanTabularPage works out the read that covers a page of a tabular file.
//
// page is the one-based number the caller asked for, and size the page size they asked for.
// Both are assumed positive; the endpoint rejects them before reaching here, with error
// codes of its own.
func PlanTabularPage(page, size, fileSize int64) TabularPage {
	zeroBased := page - 1

	// The read starts a page early so that a partial first line can be completed from the
	// page before it. On the first page there is nothing before it to complete from.
	loadPage := zeroBased
	length := size
	if zeroBased != 0 {
		loadPage = zeroBased - 1
		length = size * 2
	}

	var offset int64
	if loadPage != 0 {
		offset = size * loadPage
	}

	return TabularPage{
		Page:       zeroBased,
		Pages:      pageCount(size, fileSize),
		LoadOffset: offset,
		LoadLength: length,
	}
}

// pageCount is how many pages of the given size a file divides into, rounding up.
func pageCount(size, fileSize int64) int64 {
	if size <= 0 {
		return 0
	}
	return (fileSize + size - 1) / size
}

// TrimToWholeLines cuts a chunk back to the lines that belong to the page.
//
// The leading edge is moved forward to just after the last line ending at or before where
// the page begins, and the trailing edge back to the last line ending in the chunk -- unless
// this is the final page, which keeps everything to the end of the file. Partial rows must
// not be parsed: a row cut in half is not a row, and the CSV reader would report its
// fragments as columns.
func TrimToWholeLines(chunk string, size int64, page TabularPage) string {
	if chunk == "" {
		return ""
	}

	// Where the page begins inside the chunk: at the start for the first page, and one
	// byte short of a page in for the rest, since those reads began a page early.
	var pageStart int64
	if page.Page != 0 {
		pageStart = size - 1
	}

	start := seekLineStart(chunk, pageStart)

	end := len(chunk)
	if page.Page != page.Pages-1 {
		end = seekLineStart(chunk, int64(len(chunk)-1))
	}

	if start >= end {
		return ""
	}
	return chunk[start:end]
}

// seekLineStart scans backwards from position for a line ending and returns the offset just
// after it, or the start of the chunk when there is none.
//
// A position past the end of the chunk is a request the Clojure service answers with an
// exception and a 500. Here it is clamped to the last byte, which turns a page read past the
// end of a short file into an empty page rather than a crash.
func seekLineStart(chunk string, position int64) int {
	if position >= int64(len(chunk)) {
		position = int64(len(chunk)) - 1
	}
	if position <= 0 {
		return 0
	}

	for i := position; i > 0; i-- {
		if chunk[i] == '\n' {
			return int(i) + 1
		}
	}
	return 0
}

// ParseDelimited parses a chunk as delimited text, one map per row keyed by column number.
//
// The keys are the column indices as decimal strings, which is the wire format: the client
// reads "0", "1" and so on. A blank chunk is one empty row rather than none, matching the
// Clojure service -- the client renders a row count and zero rows is not the same answer as
// one empty one.
//
// The separator is taken as a string rather than a rune, and an empty one is an error only
// once there is something to parse. That is where the Clojure service puts it too: read-csv
// short-circuits a blank chunk before it ever reaches (.charAt separator 0), so an empty
// separator against an empty file is not a failure there and is not one here.
func ParseDelimited(chunk, separator string) ([]map[string]string, error) {
	if strings.TrimSpace(chunk) == "" {
		return []map[string]string{{}}, nil
	}

	delimiters := []rune(separator)
	if len(delimiters) == 0 {
		return nil, fmt.Errorf("a separator is required to parse delimited text")
	}

	reader := csv.NewReader(strings.NewReader(chunk))
	reader.Comma = delimiters[0]
	// Rows of differing width are ordinary here: the file is whatever a user uploaded, and
	// the endpoint reports the widest row rather than refusing to show a ragged one.
	reader.FieldsPerRecord = -1
	// Quotes appear in the middle of unquoted fields often enough in real data that
	// refusing the file would make preview useless.
	reader.LazyQuotes = true

	var out []map[string]string
	var consumed int64

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		// encoding/csv skips an empty line; opencsv reports one as a row holding a single
		// empty column. A blank line in a data file is ordinary, and dropping it would
		// shift the index of every row after it -- so they are counted back in from the
		// bytes between the previous record and this one. Only the *leading* blank lines
		// of that span count, which is what keeps a newline inside a quoted field from
		// being mistaken for one.
		offset := reader.InputOffset()
		out = append(out, blankRows(chunk[consumed:offset])...)
		consumed = offset

		row := make(map[string]string, len(record))
		for i, value := range record {
			row[strconv.Itoa(i)] = value
		}
		out = append(out, row)
	}

	// Blank lines after the last record have no following record to be found in front of.
	return append(out, blankRows(chunk[consumed:])...), nil
}

// blankRows is one row per empty line at the start of a span, in the shape opencsv gives an
// empty line: a single column holding an empty string.
func blankRows(span string) []map[string]string {
	lines := strings.Split(span, "\n")
	// The final element is whatever followed the last newline, which is not a line yet.
	var out []map[string]string
	for _, line := range lines[:max(len(lines)-1, 0)] {
		if strings.TrimSuffix(line, "\r") != "" {
			break
		}
		out = append(out, map[string]string{"0": ""})
	}
	return out
}

// WidestRow is the number of columns in the widest row, which the response reports so a
// client can size its table before reading the rows.
func WidestRow(rows []map[string]string) int {
	widest := 0
	for _, row := range rows {
		if len(row) > widest {
			widest = len(row)
		}
	}
	return widest
}
