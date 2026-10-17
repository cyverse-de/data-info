package irodsclient

import (
	"context"
	"time"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
	"github.com/cyverse/go-irodsclient/irods/types"
)

// ObjectType is what lives at a path.
type ObjectType string

const (
	// ObjectTypeFile is a data object.
	ObjectTypeFile ObjectType = "file"
	// ObjectTypeDir is a collection.
	ObjectTypeDir ObjectType = "dir"
	// ObjectTypeNone means nothing is there, or the caller cannot see it.
	ObjectTypeNone ObjectType = "none"
)

// Permission is an access level, using the DE's names rather than iRODS'.
type Permission string

const (
	// PermissionNone means no access.
	PermissionNone Permission = ""
	// PermissionRead allows reading.
	PermissionRead Permission = "read"
	// PermissionWrite allows reading and writing.
	PermissionWrite Permission = "write"
	// PermissionOwn allows reading, writing and sharing.
	PermissionOwn Permission = "own"
)

// Entry describes one data object or collection.
type Entry struct {
	ID           int64
	Type         ObjectType
	Path         string
	Name         string
	Owner        string
	Size         int64
	CreatedAt    time.Time
	ModifiedAt   time.Time
	ChecksumAlgo string
	Checksum     []byte
}

// AVU is one iRODS metadata triple.
type AVU struct {
	Attribute string
	Value     string
	Unit      string
}

// ACLEntry is one user's access to a path.
type ACLEntry struct {
	User       string
	Zone       string
	Permission Permission
	IsGroup    bool
}

// Stat reports what is at a path, returning ObjectTypeNone rather than an error when nothing
// is there, because callers routinely ask about paths that may not exist.
func Stat(ctx context.Context, s *Session, path string) (*Entry, error) {
	entry, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (*irodsfs.Entry, error) {
		return fsys.Stat(path)
	})
	if err != nil {
		if IsNotFound(err) {
			return &Entry{Type: ObjectTypeNone, Path: path}, nil
		}
		return nil, err
	}
	return convertEntry(entry), nil
}

// List returns a collection's immediate children.
func List(ctx context.Context, s *Session, path string) ([]*Entry, error) {
	entries, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) ([]*irodsfs.Entry, error) {
		return fsys.List(path)
	})
	if err != nil {
		return nil, err
	}

	out := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, convertEntry(e))
	}
	return out, nil
}

// ListACLs returns the access entries on a path, without expanding groups into members.
func ListACLs(ctx context.Context, s *Session, path string) ([]ACLEntry, error) {
	accesses, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) ([]*types.IRODSAccess, error) {
		return fsys.ListACLs(path)
	})
	if err != nil {
		return nil, err
	}

	out := make([]ACLEntry, 0, len(accesses))
	for _, a := range accesses {
		out = append(out, ACLEntry{
			User:       a.UserName,
			Zone:       a.UserZone,
			Permission: fromIRODSAccessLevel(a.AccessLevel),
			IsGroup:    a.UserType == types.IRODSUserRodsGroup,
		})
	}
	return out, nil
}

// SetACL grants a permission on a path; PermissionNone revokes access. adminMode adds
// iRODS' -M flag, needed when the session's account does not own the path; it is explicit
// because turning it on where it is not needed would let a caller change others' access.
func SetACL(ctx context.Context, s *Session, path string, perm Permission, user, zone string, recurse, adminMode bool) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.ChangeACLs(path, toIRODSAccessLevel(perm), user, zone, recurse, adminMode)
	})
	return err
}

// SetInherit turns a collection's inherit bit on or off.
func SetInherit(ctx context.Context, s *Session, path string, inherit, recurse, adminMode bool) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.ChangeDirACLInheritance(path, inherit, recurse, adminMode)
	})
	return err
}

// ListAVUs returns a path's metadata.
func ListAVUs(ctx context.Context, s *Session, path string) ([]AVU, error) {
	metas, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) ([]*types.IRODSMeta, error) {
		return fsys.ListMetadata(path)
	})
	if err != nil {
		return nil, err
	}

	out := make([]AVU, 0, len(metas))
	for _, m := range metas {
		out = append(out, AVU{Attribute: m.Name, Value: m.Value, Unit: m.Units})
	}
	return out, nil
}

// AddAVU attaches a metadata triple to a path.
func AddAVU(ctx context.Context, s *Session, path string, avu AVU) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.AddMetadata(path, avu.Attribute, avu.Value, avu.Unit)
	})
	return err
}

// DeleteAVU removes an exact metadata triple from a path.
func DeleteAVU(ctx context.Context, s *Session, path string, avu AVU) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.DeleteMetadataByAVU(path, avu.Attribute, avu.Value, avu.Unit)
	})
	return err
}

// MakeDir creates a collection, optionally creating missing parents.
func MakeDir(ctx context.Context, s *Session, path string, recurse bool) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.MakeDir(path, recurse)
	})
	return err
}

// UserExists reports whether a user account -- not a group -- exists in the zone. The type
// is checked here because FileSystem.GetUser ignores its type argument, so a group name
// would otherwise pass.
func UserExists(ctx context.Context, s *Session, user, zone string) (bool, error) {
	found, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (*types.IRODSUser, error) {
		return fsys.GetUser(user, zone, types.IRODSUserRodsUser)
	})
	if err != nil {
		if IsNotAUser(err) {
			return false, nil
		}
		return false, err
	}
	return found != nil && found.Type != types.IRODSUserRodsGroup, nil
}

// GroupExists reports whether a group of that name exists in the zone.
func GroupExists(ctx context.Context, s *Session, group, zone string) (bool, error) {
	found, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (*types.IRODSUser, error) {
		return fsys.GetUser(group, zone, types.IRODSUserRodsGroup)
	})
	if err != nil {
		if IsNotAUser(err) {
			return false, nil
		}
		return false, err
	}
	return found != nil && found.Type == types.IRODSUserRodsGroup, nil
}

// ListUserGroups returns the names of the groups a user belongs to.
func ListUserGroups(ctx context.Context, s *Session, user, zone string) ([]string, error) {
	return Do(ctx, s, func(fsys *irodsfs.FileSystem) ([]string, error) {
		return fsys.ListUserGroupNames(zone, user)
	})
}

// ServerVersion reports the connected iRODS server's release version.
func ServerVersion(ctx context.Context, s *Session) (string, error) {
	v, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (*types.IRODSVersion, error) {
		return fsys.GetServerVersion()
	})
	if err != nil {
		return "", err
	}
	return v.ReleaseVersion, nil
}

// convertEntry maps a go-irodsclient entry onto ours. Its Checksum is decoded bytes, so the
// stat md5 field must use the catalog's data_checksum text instead.
func convertEntry(e *irodsfs.Entry) *Entry {
	t := ObjectTypeFile
	if e.Type == irodsfs.DirectoryEntry {
		t = ObjectTypeDir
	}
	return &Entry{
		ID:           e.ID,
		Type:         t,
		Path:         e.Path,
		Name:         e.Name,
		Owner:        e.Owner,
		Size:         e.Size,
		CreatedAt:    e.CreateTime,
		ModifiedAt:   e.ModifyTime,
		ChecksumAlgo: string(e.CheckSumAlgorithm),
		Checksum:     e.CheckSum,
	}
}

// toIRODSAccessLevel maps the DE's permission names onto iRODS'.
func toIRODSAccessLevel(p Permission) types.IRODSAccessLevelType {
	switch p {
	case PermissionOwn:
		return types.IRODSAccessLevelOwner
	case PermissionWrite:
		return types.IRODSAccessLevelModifyObject
	case PermissionRead:
		return types.IRODSAccessLevelReadObject
	default:
		return types.IRODSAccessLevelNull
	}
}

// fromIRODSAccessLevel maps iRODS' access levels, in any of their spellings ("read object",
// "read_object"), onto the DE's permission names; anything below read is no access.
func fromIRODSAccessLevel(l types.IRODSAccessLevelType) Permission {
	switch types.GetIRODSAccessLevelType(string(l)) {
	case types.IRODSAccessLevelOwner:
		return PermissionOwn
	case types.IRODSAccessLevelModifyObject:
		return PermissionWrite
	case types.IRODSAccessLevelReadObject:
		return PermissionRead
	default:
		return PermissionNone
	}
}
