package irodsclient

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"
)

// countingWriter records the size of every write, each of which is one iRODS round trip.
// Like a file handle, and unlike an embedded bytes.Buffer, it has no ReadFrom.
type countingWriter struct {
	written bytes.Buffer
	writes  []int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, len(p))
	return w.written.Write(p)
}

// countingReader stands in for a file handle, recording the size of every read requested.
type countingReader struct {
	io.Reader
	reads []int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads = append(r.reads, len(p))
	return r.Reader.Read(p)
}

func (r *countingReader) Close() error { return nil }

func TestTransfersUseFullBuffers(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"smaller than one buffer", 1000},
		{"exactly one buffer", transferBufferSize},
		{"several buffers and a remainder", 2*transferBufferSize + 17},
	}

	for _, tt := range tests {
		data := bytes.Repeat([]byte{'x'}, tt.size)
		wantTrips := (tt.size + transferBufferSize - 1) / transferBufferSize

		t.Run("upload "+tt.name, func(t *testing.T) {
			// OneByteReader is the worst case a multipart part could be.
			var w countingWriter
			n, err := copyInFullWrites(&w, iotest.OneByteReader(bytes.NewReader(data)))
			if err != nil {
				t.Fatalf("copyInFullWrites: %v", err)
			}
			if int(n) != tt.size || !bytes.Equal(w.written.Bytes(), data) {
				t.Fatalf("copied %d bytes, want %d, and the contents must survive", n, tt.size)
			}
			if len(w.writes) != wantTrips {
				t.Errorf("%d writes, want %d: %v", len(w.writes), wantTrips, w.writes)
			}
		})

		t.Run("download "+tt.name, func(t *testing.T) {
			handle := &countingReader{Reader: bytes.NewReader(data)}
			var out bytes.Buffer
			n, err := io.Copy(&out, &fileReader{handle: handle, path: "/zone/f", user: "u"})
			if err != nil {
				t.Fatalf("io.Copy: %v", err)
			}
			if int(n) != tt.size || !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("copied %d bytes, want %d, and the contents must survive", n, tt.size)
			}
			for _, size := range handle.reads {
				if size != transferBufferSize {
					t.Fatalf("a read asked for %d bytes, want %d: %v", size, transferBufferSize, handle.reads)
				}
			}
		})
	}
}
