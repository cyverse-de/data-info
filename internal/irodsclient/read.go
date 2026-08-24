package irodsclient

import (
	"context"
	"errors"
	"fmt"
	"io"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
)

// MaxChunkSize bounds a single positional read.
//
// The reference allocates whatever the caller asked for, which on the JVM is an
// OutOfMemoryError and a 500 for an absurd size. Here it would be an allocation the runtime
// cannot recover from, so the request is refused instead. Callers narrow the length to what
// the file actually holds before reaching this, so the bound is only ever met by a request
// for tens of megabytes of a file that large. See docs/deferred-fixes.md.
const MaxChunkSize = 64 << 20

// ReadAt returns up to length bytes of a data object starting at offset.
//
// A short read is not an error: it means the file ended. The caller decides what a partial
// chunk means, which for the chunking endpoints is "this is the last page".
func ReadAt(ctx context.Context, s *Session, path string, offset, length int64) ([]byte, error) {
	if offset < 0 {
		return nil, fmt.Errorf("irodsclient: a non-negative offset is required, got %d", offset)
	}
	if length < 0 {
		return nil, fmt.Errorf("irodsclient: a non-negative length is required, got %d", length)
	}
	if length > MaxChunkSize {
		return nil, fmt.Errorf("irodsclient: %d bytes is more than the %d this will read at once", length, MaxChunkSize)
	}
	if length == 0 {
		return nil, nil
	}

	return DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) ([]byte, error) {
		handle, err := fsys.OpenFile(path, "", "r")
		if err != nil {
			return nil, err
		}
		defer handle.Close() //nolint:errcheck // nothing was written

		buffer := make([]byte, length)
		n, err := handle.ReadAt(buffer, offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return buffer[:n], nil
	})
}

// OpenReader opens a data object for streaming.
//
// The returned reader borrows the session, so it must be closed before the session is
// returned to the pool. Downloads use this rather than ReadFile: a data object is as large
// as whatever a user put there, and holding one in memory to hand it to the client would
// size the service to its largest file.
func OpenReader(ctx context.Context, s *Session, path string) (io.ReadCloser, error) {
	handle, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (*irodsfs.FileHandle, error) {
		return fsys.OpenFile(path, "", "r")
	})
	if err != nil {
		return nil, err
	}
	return &fileReader{handle: handle, path: path, user: s.ClientUser()}, nil
}

// fileReader adapts an iRODS file handle to io.ReadCloser, translating its errors into the
// service's vocabulary the way every other call through this package does.
type fileReader struct {
	handle *irodsfs.FileHandle
	path   string
	user   string
}

func (r *fileReader) Read(p []byte) (int, error) {
	n, err := r.handle.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	return n, Translate(err, r.path, r.user)
}

func (r *fileReader) Close() error { return r.handle.Close() }
