package service

import (
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/google/uuid"
)

// TempUploadName is the marker in the name of an object an upload is still streaming into.
// It is part of the contract with whoever sweeps up after an interrupted upload, so it is
// spelled the way the reference spelled it.
const TempUploadName = ".partial-"

// TempUploadPath returns a hidden path in a destination's own collection to stream an
// upload into before publishing it under its real name.
//
// Staying in the same collection is what makes the final rename a catalog-only operation:
// it is atomic, and the object's access list survives it untouched, so no permission repair
// is needed afterwards. A name is only hidden by convention here -- the leading dot is what
// keeps a half-finished upload out of the UI's listings.
//
// When the hidden name would push the object past what iRODS accepts, the destination's own
// name is dropped from it rather than truncated, which keeps the result unique.
func TempUploadPath(dest string) string {
	dir := paths.Dir(dest)
	suffix := TempUploadName + uuid.NewString()

	name := "." + paths.Base(dest) + suffix
	candidate := paths.Join(dir, name)

	if len(name) > paths.MaxFilenameLength || len(candidate) > paths.MaxPathLength {
		return paths.Join(dir, suffix)
	}
	return candidate
}
