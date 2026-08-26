// Package irodsclient wraps github.com/cyverse/go-irodsclient for data-info.
//
// It owns three things the rest of the service should not have to think about: how
// connections are pooled when every request may act as a different iRODS user, how a
// library that takes no context.Context is made cancellable, and how iRODS errors become
// the DE's ERR_* vocabulary.
package irodsclient

import (
	"errors"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse/go-irodsclient/irods/common"
	"github.com/cyverse/go-irodsclient/irods/types"
)

// Translate maps a go-irodsclient error onto the DE error vocabulary. path and user are
// folded into the envelope when non-empty, matching the keys the Clojure validators
// attached to the same conditions.
//
// Each mapping is chosen to match the error_code the Clojure service would have produced
// for the same condition, not the code that reads best. The status table is preserved
// bug-for-bug (see internal/apierror), so picking a different code here is a second,
// quieter way to change a response's status. CAT_NO_ACCESS_PERMISSION is the example
// worth remembering: mapping it to ERR_FORBIDDEN would answer 403, where the Clojure
// validators reach that condition as ERR_NOT_READABLE or ERR_NOT_WRITEABLE and answer 500.
func Translate(err error, path, user string) error {
	if err == nil {
		return nil
	}

	// Already ours, from a validator that ran before the call.
	var apiErr *apierror.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case types.IsFileNotFoundError(err):
		return withPath(apierror.New(apierror.ErrDoesNotExist), path).WithCause(err)
	case types.IsFileAlreadyExistError(err):
		return withPath(apierror.New(apierror.ErrExists), path).WithCause(err)
	case types.IsUserNotFoundError(err):
		return withUser(apierror.New(apierror.ErrNotAUser), user).WithCause(err)
	case types.IsTicketNotFoundError(err):
		return apierror.New(apierror.ErrTicketDoesNotExist).WithCause(err)
	case types.IsCollectionNotEmptyError(err):
		return withPath(apierror.New(apierror.ErrRequestFailed), path).
			With("reason", err.Error()).WithCause(err)

	// The analogue of the Clojure catch-jargon-io-exceptions macro, which turned a
	// JargonException wrapping an IOException into ERR_UNAVAILABLE.
	case types.IsConnectionError(err),
		types.IsConnectionConfigError(err),
		types.IsConnectionPoolFullError(err),
		types.IsAuthError(err):
		return apierror.New(apierror.ErrUnavailable).
			With("reason", "iRODS is unavailable: "+err.Error()).WithCause(err)
	}

	var irodsErr *types.IRODSError
	if errors.As(err, &irodsErr) {
		return translateIRODSCode(irodsErr, err, path, user)
	}

	return apierror.New(apierror.ErrRequestFailed).With("reason", err.Error()).WithCause(err)
}

// translateIRODSCode maps a catalog error code that the typed predicates do not cover.
func translateIRODSCode(irodsErr *types.IRODSError, cause error, path, user string) error {
	switch irodsErr.Code {
	case common.CAT_UNKNOWN_FILE, common.CAT_UNKNOWN_COLLECTION:
		return withPath(apierror.New(apierror.ErrDoesNotExist), path).WithCause(cause)

	case common.CAT_NAME_EXISTS_AS_DATAOBJ, common.CAT_NAME_EXISTS_AS_COLLECTION:
		return withPath(apierror.New(apierror.ErrExists), path).WithCause(cause)

	case common.CAT_NO_ACCESS_PERMISSION, common.CAT_INSUFFICIENT_PRIVILEGE_LEVEL, common.SYS_NO_API_PRIV:
		// Deliberately ERR_NOT_READABLE rather than ERR_FORBIDDEN: that is the code the
		// Clojure validators raise when iRODS refuses access, and it answers 500 where
		// ERR_FORBIDDEN would answer 403. See the note on Translate.
		return withUser(withPath(apierror.New(apierror.ErrNotReadable), path), user).WithCause(cause)

	case common.CAT_INVALID_USER, common.CAT_INVALID_GROUP:
		return withUser(apierror.New(apierror.ErrNotAUser), user).WithCause(cause)

	default:
		return withPath(apierror.New(apierror.ErrRequestFailed), path).
			With("reason", irodsErr.Error()).WithCause(cause)
	}
}

func withPath(e *apierror.Error, path string) *apierror.Error {
	if path == "" {
		return e
	}
	return e.With("path", path)
}

func withUser(e *apierror.Error, user string) *apierror.Error {
	if user == "" {
		return e
	}
	return e.With("user", user)
}

// IsNotFound reports whether err is a translated "no such path" error. Callers that treat
// absence as an ordinary answer rather than a failure use this instead of comparing codes.
func IsNotFound(err error) bool { return hasCode(err, apierror.ErrDoesNotExist) }

// IsNotAUser reports whether err is a translated "no such account" error.
func IsNotAUser(err error) bool { return hasCode(err, apierror.ErrNotAUser) }

// IsUnavailable reports whether err means iRODS could not be reached or would not
// authenticate us, as opposed to refusing a specific request.
func IsUnavailable(err error) bool { return hasCode(err, apierror.ErrUnavailable) }

func hasCode(err error, code apierror.Code) bool {
	var apiErr *apierror.Error
	return errors.As(err, &apiErr) && apiErr.Code == code
}
