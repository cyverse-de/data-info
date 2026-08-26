package paths

// The limits iRODS names are held to. They come from clj-jargon, which the Clojure service
// validated every write against, and they are what the ERR_BAD_*_LENGTH codes report.
const (
	// MaxPathLength bounds a whole path.
	MaxPathLength = 1067

	// MaxDirLength bounds the collection part of a path.
	MaxDirLength = 640

	// MaxFilenameLength bounds the last component. It is what is left of a path once the
	// collection has taken its share, which is how clj-jargon derives it.
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

// CheckLength reports the first limit a path exceeds.
//
// The order is the Clojure service's: the whole path first, then the collection, then the
// last component. A path can break more than one, and which one is reported is part of what
// callers see.
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
