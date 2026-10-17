package service

import (
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/google/uuid"
)

// TempUploadName marks the name of an object an upload is still streaming into. It is
// spelled as the Clojure service spelled it.
const TempUploadName = ".partial-"

// TempUploadPath returns a hidden path beside dest to stream an upload into. Staying in the
// same collection keeps the final rename atomic and the ACL intact; if the name would be too
// long, dest's base name is dropped rather than truncated, keeping the result unique.
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
