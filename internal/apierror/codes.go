package apierror

// Code is a DE error_code string. The vocabulary is shared across DE services and
// appears verbatim in response bodies, so these values are a wire contract.
type Code string

// Codes defined by clojure-commons (clojure_commons/error_codes.clj). Only the subset
// data-info actually emits is declared here; add others as endpoints need them.
const (
	ErrBadOrMissingField  Code = "ERR_BAD_OR_MISSING_FIELD"
	ErrBadQueryParameter  Code = "ERR_BAD_QUERY_PARAMETER"
	ErrBadRequest         Code = "ERR_BAD_REQUEST"
	ErrConfigInvalid      Code = "ERR_CONFIG_INVALID"
	ErrConflict           Code = "ERR_CONFLICT"
	ErrDoesNotExist       Code = "ERR_DOES_NOT_EXIST"
	ErrExists             Code = "ERR_EXISTS"
	ErrForbidden          Code = "ERR_FORBIDDEN"
	ErrIllegalArgument    Code = "ERR_ILLEGAL_ARGUMENT"
	ErrIncompleteRename   Code = "ERR_INCOMPLETE_RENAME"
	ErrInvalidJSON        Code = "ERR_INVALID_JSON"
	ErrMissingQueryParam  Code = "ERR_MISSING_QUERY_PARAMETER"
	ErrNotAFile           Code = "ERR_NOT_A_FILE"
	ErrNotAFolder         Code = "ERR_NOT_A_FOLDER"
	ErrNotAUser           Code = "ERR_NOT_A_USER"
	ErrNotAuthorized      Code = "ERR_NOT_AUTHORIZED"
	ErrNotFound           Code = "ERR_NOT_FOUND"
	ErrNotOwner           Code = "ERR_NOT_OWNER"
	ErrNotReadable        Code = "ERR_NOT_READABLE"
	ErrNotWriteable       Code = "ERR_NOT_WRITEABLE"
	ErrRequestFailed      Code = "ERR_REQUEST_FAILED"
	ErrTicketDoesNotExist Code = "ERR_TICKET_DOES_NOT_EXIST"
	ErrTicketExists       Code = "ERR_TICKET_EXISTS"
	ErrTooManyResults     Code = "ERR_TOO_MANY_RESULTS"
	ErrUnavailable        Code = "ERR_UNAVAILABLE"
	ErrUncheckedException Code = "ERR_UNCHECKED_EXCEPTION"

	// Codes used by the Clojure service that clojure-commons never defined. They are
	// bare strings there, so http-status-for has no entry for them and they can only
	// ever produce a 500. Reproduced deliberately; see statusFor.
	ErrBadBasenameLength Code = "ERR_BAD_BASENAME_LENGTH"
	ErrBadDirnameLength  Code = "ERR_BAD_DIRNAME_LENGTH"
	ErrBadPathLength     Code = "ERR_BAD_PATH_LENGTH"
	ErrChunkTooSmall     Code = "ERR_CHUNK_TOO_SMALL"
	ErrInvalidPage       Code = "ERR_INVALID_PAGE"
	ErrPageNotPos        Code = "ERR_PAGE_NOT_POS"
	ErrTooManyPaths      Code = "ERR_TOO_MANY_PATHS"
)
