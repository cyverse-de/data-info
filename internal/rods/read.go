package rods

import (
	"context"
	"io"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// MaxReadableFileSize bounds what ReadFile will return.
//
// These files are configuration a person wrote -- a CSV of metadata, a path list -- so this
// is generous for that and small enough that pointing the endpoint at a data file fails
// rather than filling memory.
const MaxReadableFileSize = 32 << 20

// ReadFile returns a data object's contents, up to MaxReadableFileSize.
func (s *Scope) ReadFile(ctx context.Context, path string) ([]byte, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.ReadFile(ctx, sess, normalizePath(path), MaxReadableFileSize)
}

// ReadAt returns up to length bytes of a data object starting at offset.
//
// The chunking endpoints read this way rather than opening a stream: they want one span of
// one file, and a positional read is a single round trip where a stream would be an open, a
// seek and a close.
func (s *Scope) ReadAt(ctx context.Context, path string, offset, length int64) ([]byte, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.ReadAt(ctx, sess, normalizePath(path), offset, length)
}

// OpenFile opens a data object for streaming.
//
// The reader borrows the scope's session, so it must be closed before the scope is. A
// handler that streams a download therefore closes it before returning, not after.
func (s *Scope) OpenFile(ctx context.Context, path string) (io.ReadCloser, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.OpenReader(ctx, sess, normalizePath(path))
}

// ChildEntry is one member of a collection, with enough to know what it is.
type ChildEntry struct {
	Path  string
	IsDir bool
}

// ChildEntries lists what a collection holds, saying which members are collections.
//
// Children answers the same question without the types, and most callers only need paths.
// This exists for the walks that have to descend, which would otherwise cost a stat per
// member to find out where to go.
func (s *Scope) ChildEntries(ctx context.Context, path string) ([]ChildEntry, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}

	entries, err := irodsclient.List(ctx, sess, normalizePath(path))
	if err != nil {
		return nil, err
	}

	out := make([]ChildEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ChildEntry{Path: entry.Path, IsDir: entry.Type == irodsclient.ObjectTypeDir})
	}
	return out, nil
}
