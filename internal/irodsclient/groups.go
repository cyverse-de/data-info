package irodsclient

import (
	"context"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
	"github.com/cyverse/go-irodsclient/irods/types"
)

// CreateGroup makes a new iRODS group.
func CreateGroup(ctx context.Context, s *Session, name, zone string) error {
	_, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		_, err := fsys.CreateUser(name, zone, types.IRODSUserRodsGroup)
		return struct{}{}, err
	})
	return err
}

// DeleteGroup removes an iRODS group.
func DeleteGroup(ctx context.Context, s *Session, name, zone string) error {
	_, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.RemoveUser(name, zone, types.IRODSUserRodsGroup)
	})
	return err
}

// AddGroupMember puts a user in a group.
func AddGroupMember(ctx context.Context, s *Session, group, user, zone string) error {
	_, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.AddGroupMember(group, user, zone)
	})
	return err
}

// RemoveGroupMember takes a user out of a group.
func RemoveGroupMember(ctx context.Context, s *Session, group, user, zone string) error {
	_, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.RemoveGroupMember(group, user, zone)
	})
	return err
}

// ListGroupMembers returns the names of a group's members.
func ListGroupMembers(ctx context.Context, s *Session, group, zone string) ([]string, error) {
	return Do(ctx, s, func(fsys *irodsfs.FileSystem) ([]string, error) {
		return fsys.ListGroupMemberNames(zone, group)
	})
}
