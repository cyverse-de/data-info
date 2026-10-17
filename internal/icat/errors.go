package icat

import "fmt"

// QueryError is a catalog query that failed to run. The database's own message names
// tables, columns and constraints, so it belongs in the logs and not in a response.
type QueryError struct {
	// Op says what was being looked up, in terms a caller would recognise.
	Op  string
	Err error
}

func (e *QueryError) Error() string { return "icat: " + e.Op + ": " + e.Err.Error() }

func (e *QueryError) Unwrap() error { return e.Err }

// queryError wraps a failed query's error with a description of what it was for.
func queryError(err error, format string, args ...any) error {
	return &QueryError{Op: fmt.Sprintf(format, args...), Err: err}
}
