package irodsclient

import (
	"context"
	"fmt"
	"io"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
	irods_fs "github.com/cyverse/go-irodsclient/irods/fs"
)

// uploadBufferSize is the chunk the reader is drained in. It is a compromise: iRODS pays a
// round trip per write, so small buffers are slow, while a large one is held for the whole
// upload on every concurrent request.
const uploadBufferSize = 1 << 20

// WriteFile streams r into a new data object at path and returns the number of bytes
// written.
//
// The whole copy runs inside one Do, so cancelling the request abandons the transfer and
// discards the session rather than leaving a half-written object attached to a connection
// the next caller would inherit. Nothing is buffered on local disk: the reader here is the
// request's own multipart part.
func WriteFile(ctx context.Context, s *Session, path string, r io.Reader, resource string) (int64, error) {
	return DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (int64, error) {
		handle, err := fsys.CreateFile(path, resource, "w")
		if err != nil {
			return 0, err
		}

		written, copyErr := io.CopyBuffer(handle, r, make([]byte, uploadBufferSize))

		// Close is what commits the object, so its error matters even when the copy
		// succeeded. When the copy failed it is still called, to release the handle, but
		// the copy error is the one worth reporting.
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

// RenameFile moves a data object to another path.
//
// Used to publish an upload written to a temporary name. Both paths are expected to be in
// the same collection, which keeps this a catalog-only operation and means the object's
// access list needs no repair afterwards.
func RenameFile(ctx context.Context, s *Session, from, to string) error {
	_, err := DoPath(ctx, s, from, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.RenameFileToFile(from, to)
	})
	return err
}

// RemoveFile deletes a data object. With force it is removed outright rather than moved to
// the trash.
func RemoveFile(ctx context.Context, s *Session, path string, force bool) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.RemoveFile(path, force)
	})
	return err
}

// Checksum asks iRODS for a data object's checksum, computing and recording it in the
// catalog when there is not one already.
//
// This has to be asked for. The reference's client computed a checksum as part of every
// upload, and the stat endpoints read that value straight out of the catalog column, so an
// object written without one reports an empty md5 for the rest of its life. The hashing
// happens on the resource server, so the file's bytes do not cross the wire again.
//
// The value is returned for the caller's benefit; the reason to call this is the recording.
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
// cache.
func ExistsFile(ctx context.Context, s *Session, path string) (bool, error) {
	found, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (bool, error) {
		return fsys.ExistsFileFresh(path), nil
	})
	if err != nil {
		return false, fmt.Errorf("checking for a data object at %q: %w", path, err)
	}
	return found, nil
}
