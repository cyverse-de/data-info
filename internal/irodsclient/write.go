package irodsclient

import (
	"bufio"
	"context"
	"fmt"
	"io"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
	irods_fs "github.com/cyverse/go-irodsclient/irods/fs"
	"github.com/cyverse/go-irodsclient/irods/types"
)

// transferBufferSize is how much an upload or download moves per iRODS round trip. Every
// Read and Write on a go-irodsclient handle is one RPC, so this sets the round trips per
// GiB; the cost is one buffer held for each transfer in flight.
const transferBufferSize = 4 << 20

// WriteFile streams r into a new data object at path and returns the bytes written. The copy
// runs as one cancellable call, so a cancelled request discards the session rather than
// leave a half-written handle on a connection the next caller would inherit.
func WriteFile(ctx context.Context, s *Session, path string, r io.Reader, resource string) (int64, error) {
	return doLeased(ctx, s, path, transferConn, func(fsys *irodsfs.FileSystem) (int64, error) {
		handle, err := fsys.CreateFile(path, resource, "w")
		if err != nil {
			return 0, err
		}

		written, copyErr := copyInFullWrites(handle, r)

		// Close commits the object, so its error matters even after a successful copy;
		// after a failed one the copy error is the one worth reporting.
		closeErr := handle.Close()
		if copyErr != nil {
			return written, copyErr
		}
		if closeErr != nil {
			return written, closeErr
		}
		return written, nil
	})
}

// copyInFullWrites copies r to w in writes of transferBufferSize, whatever size r's reads
// come back in. A multipart part never returns more than 4 KiB at a time.
func copyInFullWrites(w io.Writer, r io.Reader) (int64, error) {
	buffered := bufio.NewWriterSize(w, transferBufferSize)
	written, err := io.Copy(buffered, r)
	if err != nil {
		return written, err
	}
	return written, buffered.Flush()
}

// ReadFile returns a data object's whole contents, failing if it exceeds limit. It is for
// small files the service interprets, such as metadata CSVs and path lists, not downloads.
func ReadFile(ctx context.Context, s *Session, path string, limit int64) ([]byte, error) {
	return doLeased(ctx, s, path, transferConn, func(fsys *irodsfs.FileSystem) ([]byte, error) {
		handle, err := fsys.OpenFile(path, "", "r")
		if err != nil {
			return nil, err
		}
		defer handle.Close() //nolint:errcheck // nothing was written

		// One byte past the limit, so an oversize file fails rather than arriving
		// truncated: a metadata CSV cut off mid-row would be applied as far as it went.
		contents, err := io.ReadAll(io.LimitReader(handle, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(contents)) > limit {
			return nil, fmt.Errorf("%q is larger than the %d bytes this endpoint will read", path, limit)
		}
		return contents, nil
	})
}

// RenameFile moves a data object to another path, to publish an upload written under a
// temporary name. Both paths are expected in one collection, so the access list needs no
// repair afterwards.
func RenameFile(ctx context.Context, s *Session, from, to string) error {
	_, err := DoPath(ctx, s, from, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.RenameFileToFile(from, to)
	})
	return err
}

// Move renames a collection or data object. Across collections the access list travels
// unchanged, so the caller has to repair permissions afterwards.
func Move(ctx context.Context, s *Session, from, to string) error {
	_, err := DoPath(ctx, s, from, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		entry, err := fsys.Stat(from)
		if err != nil {
			return struct{}{}, err
		}

		if entry.IsDir() {
			return struct{}{}, fsys.RenameDirToDir(from, to)
		}
		return struct{}{}, fsys.RenameFileToFile(from, to)
	})
	return err
}

// RemoveDir deletes a collection. With recurse it takes everything under it.
func RemoveDir(ctx context.Context, s *Session, path string, recurse, force bool) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.RemoveDir(path, recurse, force)
	})
	return err
}

// Inherits reports whether a collection passes its access list down to what is created
// inside it.
func Inherits(ctx context.Context, s *Session, path string) (bool, error) {
	inheritance, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (*types.IRODSAccessInheritance, error) {
		return fsys.GetDirACLInheritance(path)
	})
	if err != nil {
		return false, err
	}
	return inheritance != nil && inheritance.Inheritance, nil
}

// RemoveFile deletes a data object. With force it is removed outright rather than moved to
// the trash.
func RemoveFile(ctx context.Context, s *Session, path string, force bool) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.RemoveFile(path, force)
	})
	return err
}

// Checksum has iRODS compute and record a data object's checksum when it has none, and
// returns it. Uploads must call it: stat reports the catalog's checksum column verbatim, so
// an object written without one reports an empty md5 forever.
func Checksum(ctx context.Context, s *Session, path, resource string) (string, error) {
	return DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (string, error) {
		conn, err := fsys.GetMetadataConnection(true)
		if err != nil {
			return "", err
		}
		defer func() { _ = fsys.ReturnMetadataConnection(conn) }() //nolint:errcheck // the value is already in hand

		sum, err := irods_fs.GetDataObjectChecksum(conn, path, resource)
		if err != nil {
			return "", err
		}
		return sum.IRODSChecksumString, nil
	})
}

// ExistsFile reports whether a data object is at path, asking the server rather than any
// cache. Only a not-found answer means absent; any other failure is returned, because callers
// act on "absent" by giving up a cleanup or recreating the object.
func ExistsFile(ctx context.Context, s *Session, path string) (bool, error) {
	found, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (bool, error) {
		entry, err := fsys.StatFileFresh(path)
		if types.IsFileNotFoundError(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return entry.ID > 0, nil
	})
	if err != nil {
		return false, fmt.Errorf("checking for a data object at %q: %w", path, err)
	}
	return found, nil
}
