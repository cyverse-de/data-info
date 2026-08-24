package service

import (
	"reflect"
	"testing"
)

// TestDecodeChunkDropsPartialCharacters covers the edges a positional read lands in the
// middle of. A replacement character here would end up in the response as column data.
func TestDecodeChunkDropsPartialCharacters(t *testing.T) {
	// Three bytes: the U+00E9 sequence is two, the U+4E16 sequence is three.
	eacute := []byte{0xC3, 0xA9}
	world := []byte{0xE4, 0xB8, 0x96}

	tests := []struct {
		name        string
		buffer      []byte
		trimLeading bool
		want        string
	}{
		{"empty", nil, false, ""},
		{"plain ascii", []byte("abc"), false, "abc"},
		{"whole sequence", eacute, false, "é"},
		{"leading continuation kept at the start of the file", eacute[1:], false, "\xa9"},
		{"leading continuation dropped mid-file", eacute[1:], true, ""},
		{"trailing lead byte dropped", append([]byte("ab"), world[0]), false, "ab"},
		{"trailing partial sequence dropped", append([]byte("ab"), world[:2]...), false, "ab"},
		{"trailing whole sequence kept", append([]byte("ab"), world...), false, "ab世"},
		{"both edges trimmed", append(append([]byte{}, eacute[1:]...), append([]byte("ab"), world[:2]...)...), true, "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecodeChunk(tt.buffer, tt.trimLeading); got != tt.want {
				t.Errorf("DecodeChunk(%v, %v) = %q, want %q", tt.buffer, tt.trimLeading, got, tt.want)
			}
		})
	}
}

// TestPlanTabularPage pins the arithmetic, including the parts that look wrong. Beyond the
// first page the read starts a page early and is twice as long, which is how a row split
// across a page boundary is recovered.
func TestPlanTabularPage(t *testing.T) {
	tests := []struct {
		name                   string
		page, size, fileSize   int64
		wantZero, wantPages    int64
		wantOffset, wantLength int64
	}{
		{"first page of a short file", 1, 100, 250, 0, 3, 0, 100},
		{"second page reads from the start", 2, 100, 250, 1, 3, 0, 200},
		{"third page reads from one page back", 3, 100, 250, 2, 3, 100, 200},
		{"an exact multiple divides evenly", 1, 100, 200, 0, 2, 0, 100},
		{"an empty file has no pages", 1, 100, 0, 0, 0, 0, 100},
		{"a page past the end still plans a read", 5, 100, 250, 4, 3, 300, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanTabularPage(tt.page, tt.size, tt.fileSize)
			want := TabularPage{
				Page: tt.wantZero, Pages: tt.wantPages,
				LoadOffset: tt.wantOffset, LoadLength: tt.wantLength,
			}
			if got != want {
				t.Errorf("PlanTabularPage(%d, %d, %d) = %+v, want %+v",
					tt.page, tt.size, tt.fileSize, got, want)
			}
		})
	}
}

// TestTrimToWholeLines covers the cut that keeps a partial row out of the parse.
func TestTrimToWholeLines(t *testing.T) {
	tests := []struct {
		name  string
		chunk string
		size  int64
		page  TabularPage
		want  string
	}{
		{
			name:  "the first page keeps everything up to the last line ending",
			chunk: "a,1\nb,2\nc,",
			size:  10,
			page:  TabularPage{Page: 0, Pages: 2},
			want:  "a,1\nb,2\n",
		},
		{
			// Page 1 nominally begins one byte short of a page into a chunk that started a
			// page early, so the cut is back to the line ending before that.
			name:  "the last page keeps everything to the end",
			chunk: "a,1\nb,2\nc,3",
			size:  10,
			page:  TabularPage{Page: 1, Pages: 2},
			want:  "c,3",
		},
		{
			name:  "an empty chunk stays empty",
			chunk: "",
			size:  10,
			page:  TabularPage{Page: 0, Pages: 1},
			want:  "",
		},
		{
			name:  "a chunk with no line ending at all is dropped whole",
			chunk: "abcdefghij",
			size:  10,
			page:  TabularPage{Page: 0, Pages: 2},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrimToWholeLines(tt.chunk, tt.size, tt.page); got != tt.want {
				t.Errorf("TrimToWholeLines(%q) = %q, want %q", tt.chunk, got, tt.want)
			}
		})
	}
}

// TestParseDelimited covers the row shape, which is the wire contract: a map per row keyed
// by the column number as a string.
//
// The blank-line cases are the ones worth keeping. encoding/csv skips an empty line and
// opencsv reports one as a row of a single empty column, so without counting them back in,
// every row after a blank line would come back under the wrong index.
func TestParseDelimited(t *testing.T) {
	blank := map[string]string{"0": ""}

	tests := []struct {
		name      string
		chunk     string
		separator string
		want      []map[string]string
	}{
		{
			name: "blank is one empty row, not none", chunk: "  \n ", separator: ",",
			want: []map[string]string{{}},
		},
		{
			name: "comma separated", chunk: "a,b\nc,d\n", separator: ",",
			want: []map[string]string{{"0": "a", "1": "b"}, {"0": "c", "1": "d"}},
		},
		{
			name: "tab separated", chunk: "a\tb\n", separator: "\t",
			want: []map[string]string{{"0": "a", "1": "b"}},
		},
		{
			name: "ragged rows are kept as they are", chunk: "a,b,c\nd\n", separator: ",",
			want: []map[string]string{{"0": "a", "1": "b", "2": "c"}, {"0": "d"}},
		},
		{
			name: "quoted fields keep their separators", chunk: `"a,b",c` + "\n", separator: ",",
			want: []map[string]string{{"0": "a,b", "1": "c"}},
		},
		{
			name: "a blank line in the middle keeps its row", chunk: "a,b\n\nc,d\n", separator: ",",
			want: []map[string]string{{"0": "a", "1": "b"}, blank, {"0": "c", "1": "d"}},
		},
		{
			name: "a blank line at the start keeps its row", chunk: "\na,b\n", separator: ",",
			want: []map[string]string{blank, {"0": "a", "1": "b"}},
		},
		{
			name: "a blank line at the end keeps its row", chunk: "a,b\n\n", separator: ",",
			want: []map[string]string{{"0": "a", "1": "b"}, blank},
		},
		{
			name: "consecutive blank lines each keep a row", chunk: "a,b\n\n\nc,d\n", separator: ",",
			want: []map[string]string{{"0": "a", "1": "b"}, blank, blank, {"0": "c", "1": "d"}},
		},
		{
			name: "carriage returns do not hide a blank line", chunk: "a,b\r\n\r\nc,d\r\n", separator: ",",
			want: []map[string]string{{"0": "a", "1": "b"}, blank, {"0": "c", "1": "d"}},
		},
		{
			// The newlines are inside a field, not between rows, so they are not lines.
			name:  "a newline inside a quoted field is not a blank line",
			chunk: "\"a\n\nb\",c\n", separator: ",",
			want: []map[string]string{{"0": "a\n\nb", "1": "c"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDelimited(tt.chunk, tt.separator)
			if err != nil {
				t.Fatalf("ParseDelimited: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseDelimited(%q) = %v, want %v", tt.chunk, got, tt.want)
			}
		})
	}
}

// TestParseDelimitedNeedsASeparatorOnlyWhenThereIsSomethingToParse pins where the reference
// puts that failure: read-csv short-circuits a blank chunk before reaching (.charAt
// separator 0), so an empty separator is not an error until there is a row to split.
func TestParseDelimitedNeedsASeparatorOnlyWhenThereIsSomethingToParse(t *testing.T) {
	if _, err := ParseDelimited("   ", ""); err != nil {
		t.Errorf("a blank chunk with no separator: %v, want no error", err)
	}
	if _, err := ParseDelimited("a,b\n", ""); err == nil {
		t.Error("a chunk with no separator returned no error")
	}
}

// TestWidestRow covers the column count the client sizes its table from.
func TestWidestRow(t *testing.T) {
	tests := []struct {
		name string
		rows []map[string]string
		want int
	}{
		{"no rows", nil, 0},
		{"one empty row", []map[string]string{{}}, 0},
		{"the widest wins", []map[string]string{{"0": "a"}, {"0": "a", "1": "b", "2": "c"}}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WidestRow(tt.rows); got != tt.want {
				t.Errorf("WidestRow = %d, want %d", got, tt.want)
			}
		})
	}
}
