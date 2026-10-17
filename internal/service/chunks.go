package service

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DecodeChunk decodes a chunk read at an arbitrary offset as UTF-8, dropping partial
// characters at its edges rather than emitting U+FFFD into the CSV. trimLeading is false when
// the read started at the beginning of the file.
func DecodeChunk(buffer []byte, trimLeading bool) string {
	if len(buffer) == 0 {
		return ""
	}

	start := 0
	if trimLeading {
		// A UTF-8 lead byte is followed by at most three continuation bytes.
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

// PlanTabularPage works out the read that covers a page of a tabular file. page is
// one-based; page and size must already be validated as positive.
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

// TrimToWholeLines cuts a chunk to the whole lines belonging to the page (to EOF on the final
// page), so the CSV reader never sees half a row.
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

// seekLineStart returns the offset just after the last newline at or before position, or 0.
// A position past the end is clamped, so a page past the end of a short file reads as empty.
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

// ParseDelimited parses a chunk as delimited text, one map per row keyed by decimal column
// index (the wire format). As in the Clojure service, a blank chunk is one empty row and an
// empty separator is an error only when there is something to parse.
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

		// encoding/csv skips empty lines, which would shift every later row's index, so
		// restore them from the bytes since the previous record. Only the span's leading
		// blank lines count, so a newline inside a quoted field is not mistaken for one.
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

// blankRows returns one row per empty line at the start of span, holding a single empty
// column as the Clojure service's CSV parser reported it.
func blankRows(span string) []map[string]string {
	lines := strings.Split(span, "\n")
	var out []map[string]string
	// The final element is whatever followed the last newline, which is not a line yet.
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
