package rods

import (
	"context"
	"io"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// MaxReadableFileSize bounds what ReadFile will return: generous for a hand-written metadata
// CSV or path list, small enough that pointing it at a data file fails rather than filling
// memory.
const MaxReadableFileSize = 32 << 20

// ReadFile returns a data object's contents, up to MaxReadableFileSize.
func (s *Scope) ReadFile(ctx context.Context, path string) ([]byte, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.ReadFile(ctx, sess, normalizePath(path), MaxReadableFileSize)
}

// ReadAt returns up to length bytes of a data object starting at offset, in one round trip
// rather than a stream's open, seek and close.
func (s *Scope) ReadAt(ctx context.Context, path string, offset, length int64) ([]byte, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.ReadAt(ctx, sess, normalizePath(path), offset, length)
}

// OpenFile opens a data object for streaming. The reader borrows the scope's session, so it
// must be closed before the scope is.
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

// ChildEntries lists what a collection holds, saying which members are collections, so a
// walk that descends needs no stat per member.
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
