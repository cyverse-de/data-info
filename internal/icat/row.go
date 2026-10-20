package icat

import (
	"database/sql"
	"strconv"
	"time"
)

// ObjectType distinguishes the two kinds of catalog object. The values are the ones the
// catalog itself uses, because they arrive straight from a query.
type ObjectType string

const (
	ObjectTypeCollection ObjectType = "collection"
	ObjectTypeDataObject ObjectType = "dataobject"
)

// Row is one catalog item, as the item and listing queries return it. Timestamps stay as
// the catalog's epoch-seconds text and are parsed on demand.
type Row struct {
	ObjectID     int64          `db:"object_id"`
	Type         ObjectType     `db:"type"`
	UUID         sql.NullString `db:"uuid"`
	FullPath     string         `db:"full_path"`
	BaseName     string         `db:"base_name"`
	InfoType     sql.NullString `db:"info_type"`
	DataSize     int64          `db:"data_size"`
	CreateTS     string         `db:"create_ts"`
	ModifyTS     string         `db:"modify_ts"`
	AccessTypeID int64          `db:"access_type_id"`

	// DataChecksum is the catalog's data_checksum column, reported as the stat md5 field.
	// It is passed through verbatim: iRODS may hold a base64 SHA-2 digest with an
	// algorithm prefix rather than an MD5, and callers compare the string.
	DataChecksum sql.NullString `db:"data_checksum"`

	// TotalCount is the count of rows the query would have returned without paging. Only
	// the paged listings set it.
	TotalCount sql.NullInt64 `db:"total_count"`
}

// IsCollection reports whether the row is a collection.
func (r Row) IsCollection() bool { return r.Type == ObjectTypeCollection }

// Created is the creation time.
func (r Row) Created() time.Time { return epochSeconds(r.CreateTS) }

// Modified is the modification time.
func (r Row) Modified() time.Time { return epochSeconds(r.ModifyTS) }

// CreatedMillis is the creation time as milliseconds since the epoch, which is what the
// API reports.
func (r Row) CreatedMillis() int64 { return epochMillis(r.CreateTS) }

// ModifiedMillis is the modification time as milliseconds since the epoch.
func (r Row) ModifiedMillis() int64 { return epochMillis(r.ModifyTS) }

// ParseTimestamp converts a catalog timestamp (epoch seconds) to epoch milliseconds. ok is
// false for an unparseable column, so callers can tell it from a genuine epoch zero.
func ParseTimestamp(s string) (millis int64, ok bool) {
	seconds, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return seconds * 1000, true
}

func epochMillis(s string) int64 {
	millis, _ := ParseTimestamp(s)
	return millis
}

func epochSeconds(s string) time.Time {
	millis, ok := ParseTimestamp(s)
	if !ok {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

// Perm is one user's access to one path.
type Perm struct {
	ObjectID     int64  `db:"object_id"`
	FullPath     string `db:"full_path"`
	UserName     string `db:"user_name"`
	Zone         string `db:"zone_name"`
	AccessTypeID int64  `db:"access_type_id"`
}

// Access levels, as the catalog's r_tokn_main numbers them; the DE exposes only these three
// of iRODS's many levels.
const (
	AccessRead  int64 = 1050
	AccessWrite int64 = 1120
	AccessOwn   int64 = 1200
)

// Permission is the DE's name for an access level.
type Permission string

// The DE's permission names.
const (
	PermissionNone  Permission = ""
	PermissionRead  Permission = "read"
	PermissionWrite Permission = "write"
	PermissionOwn   Permission = "own"
)

// PermissionOf maps a catalog access level onto the DE's names. Matching is exact, as in the
// Clojure service, so an intermediate level such as delete_object (1130) reports as no
// permission even though it sits above write.
func PermissionOf(accessTypeID int64) Permission {
	switch accessTypeID {
	case AccessOwn:
		return PermissionOwn
	case AccessWrite:
		return PermissionWrite
	case AccessRead:
		return PermissionRead
	default:
		return PermissionNone
	}
}

// Permission is the row's access level for the requesting user.
func (r Row) Permission() Permission { return PermissionOf(r.AccessTypeID) }

// Permission is the entry's access level.
func (p Perm) Permission() Permission { return PermissionOf(p.AccessTypeID) }
