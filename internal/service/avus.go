package service

import (
	"strings"

	"github.com/cyverse-de/data-info/internal/rods"
)

// AVU units the DE reserves.
//
// ReservedUnit stands in for "no unit": iRODS stores a unit on every AVU, so an AVU with a
// blank one is recorded with this and reported back as blank. SystemUnit marks metadata this
// service manages itself, such as where something came from before it was trashed.
const (
	ReservedUnit = "ipc-reserved-unit"
	SystemUnit   = "ipc-system-avu"

	// InfoTypeUnit is the unit info-typer writes on info-type AVUs; it is not SystemUnit.
	InfoTypeUnit = "ipc-data-info"
)

// reservedAttributePrefix marks an attribute as the DE's rather than a user's; it is matched
// case-insensitively.
const reservedAttributePrefix = "ipc"

// AVU is one metadata triple as the API reports it.
type AVU struct {
	Attribute string `json:"attr"`
	Value     string `json:"value"`
	Unit      string `json:"unit"`
}

// IsReservedAVU reports whether an attribute belongs to the DE rather than to a user. It
// decides both what listings hide and what callers may not write.
func IsReservedAVU(attribute string) bool {
	return strings.HasPrefix(strings.ToLower(attribute), reservedAttributePrefix)
}

// VisibleAVU renders one AVU the way the API reports it, turning the reserved unit back into
// the blank the caller supplied.
func VisibleAVU(avu rods.AVU) AVU {
	unit := avu.Unit
	if unit == ReservedUnit {
		unit = ""
	}
	return AVU{Attribute: avu.Attribute, Value: avu.Value, Unit: unit}
}

// VisibleAVUs renders a path's AVUs, dropping the DE's own unless includeReserved is set.
func VisibleAVUs(avus []rods.AVU, includeReserved bool) []AVU {
	out := make([]AVU, 0, len(avus))
	for _, avu := range avus {
		if !includeReserved && IsReservedAVU(avu.Attribute) {
			continue
		}
		out = append(out, VisibleAVU(avu))
	}
	return out
}

// StoredAVU turns an AVU from a request into what iRODS will hold, giving a blank unit the
// reserved one so that it round-trips.
func StoredAVU(avu AVU) rods.AVU {
	unit := avu.Unit
	if strings.TrimSpace(unit) == "" {
		unit = ReservedUnit
	}
	return rods.AVU{Attribute: avu.Attribute, Value: avu.Value, Unit: unit}
}

// MetadataTargetType maps an object type onto the name the metadata service uses, which calls
// a collection a folder.
func MetadataTargetType(kind rods.ObjectType) string {
	if kind == rods.ObjectTypeDir {
		return "folder"
	}
	return "file"
}
