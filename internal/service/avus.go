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

	// InfoTypeUnit is what an info-type AVU carries. It is not SystemUnit: info-typer and
	// the reference both write this one, and every typed file in the data store has it.
	InfoTypeUnit = "ipc-data-info"
)

// reservedAttributePrefix marks an attribute as the DE's rather than a user's. Matched
// without regard to case, which is what the reference's pattern does.
const reservedAttributePrefix = "ipc"

// AVU is one metadata triple as the API reports it.
type AVU struct {
	Attribute string `json:"attr"`
	Value     string `json:"value"`
	Unit      string `json:"unit"`
}

// IsReservedAVU reports whether an attribute belongs to the DE rather than to a user.
//
// The DE keeps its own bookkeeping in the same namespace users write to, so the only thing
// separating them is this prefix. That is why it decides both what a listing hides and what a
// caller may not write.
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

// VisibleAVUs renders a path's AVUs.
//
// Without includeReserved the DE's own are dropped, which is what an ordinary caller sees:
// they are not that caller's metadata and editing them is refused anyway, so listing them
// would only invite it.
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

// MetadataTargetType maps an object type onto the name the metadata service uses.
//
// It calls a collection a folder where iRODS calls it a dir. The two services were built at
// different times and neither name is going to change.
func MetadataTargetType(kind rods.ObjectType) string {
	if kind == rods.ObjectTypeDir {
		return "folder"
	}
	return "file"
}
