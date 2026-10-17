package paths

// Path length limits, taken from clj-jargon (the Clojure iRODS library); the ERR_BAD_*_LENGTH
// codes report them.
const (
	// MaxPathLength bounds a whole path.
	MaxPathLength = 1067

	// MaxDirLength bounds the collection part of a path.
	MaxDirLength = 640

	// MaxFilenameLength bounds the last component: what a full-length collection leaves of a path.
	MaxFilenameLength = MaxPathLength - MaxDirLength
)

// LengthViolation names which limit a path exceeds, if any.
type LengthViolation string

const (
	// LengthOK means the path is within every limit.
	LengthOK LengthViolation = ""
	// LengthPath means the whole path is too long.
	LengthPath LengthViolation = "path"
	// LengthDir means the collection part is too long.
	LengthDir LengthViolation = "dir"
	// LengthBasename means the last component is too long.
	LengthBasename LengthViolation = "basename"
)

// CheckLength reports the first limit a path exceeds. The check order matters: it decides
// which error callers see for a path that breaks several limits.
func CheckLength(p string) LengthViolation {
	switch {
	case len(p) > MaxPathLength:
		return LengthPath
	case len(Dir(p)) > MaxDirLength:
		return LengthDir
	case len(Base(p)) > MaxFilenameLength:
		return LengthBasename
	}
	return LengthOK
}
