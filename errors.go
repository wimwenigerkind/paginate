package paginate

import "fmt"

// Code is a stable, machine-readable classification of an error returned by
// this package. Codes are part of the public API: they are meant to be put on
// the wire and matched by clients.
type Code string

const (
	// CodeInvalidCursor means the cursor is not a cursor this package issued:
	// bad base64, bad JSON, or an unknown format version.
	CodeInvalidCursor Code = "invalid_cursor"

	// CodeCursorMismatch means the cursor is well formed but was issued for a
	// different sort order than the one now being requested.
	CodeCursorMismatch Code = "cursor_mismatch"

	// CodeInvalidLimit means the requested limit is negative.
	CodeInvalidLimit Code = "invalid_limit"

	// CodeLimitTooLarge means the requested limit exceeds Config.MaxLimit.
	CodeLimitTooLarge Code = "limit_too_large"

	// CodeUnknownSort means the requested sort token is not in the allowlist.
	CodeUnknownSort Code = "unknown_sort"

	// CodeInvalidPage means the requested page number is negative.
	CodeInvalidPage Code = "invalid_page"

	// CodeConfig means the Allowlist or Paginator is misconfigured. This is a
	// programmer error, not a client error.
	CodeConfig Code = "config"

	// CodeUnsortableField means an allowlisted column cannot be paged on: it
	// does not exist on the model, or it is nullable, and a NULL sort key drops
	// its row from every page without raising an error.
	CodeUnsortableField Code = "unsortable_field"
)

// ClientFault reports whether the code describes bad input from the client, as
// opposed to a misconfiguration of the service itself. It is the distinction
// between answering 400 and answering 500.
func (c Code) ClientFault() bool {
	switch c {
	case CodeInvalidCursor, CodeCursorMismatch, CodeInvalidLimit,
		CodeLimitTooLarge, CodeUnknownSort, CodeInvalidPage:
		return true
	default:
		return false
	}
}

// Error is the error type returned by every exported function in this package
// that fails for a reason of its own. Errors originating in the database are
// returned unwrapped, because classifying them is not this package's job.
//
// Match errors with errors.Is against the sentinels below, which compare by
// Code, or read the Code directly to render a response.
type Error struct {
	Code Code
	// Param names the request parameter at fault: "cursor", "limit", "sort" or
	// "page". It is empty for configuration errors.
	Param string

	msg string
	err error
}

func (e *Error) Error() string {
	switch {
	case e.msg == "":
		return string(e.Code)
	case e.err == nil:
		return e.msg
	default:
		return e.msg + ": " + e.err.Error()
	}
}

// Message is the explanation without the underlying cause, for showing to a
// client. Error, which appends the cause, is for logs: the cause is often an
// encoding/json parse error whose wording describes our internals rather than
// anything the caller did.
func (e *Error) Message() string {
	if e.msg == "" {
		return string(e.Code)
	}
	return e.msg
}

func (e *Error) Unwrap() error { return e.err }

// Is reports whether target is an *Error with the same Code, so that the
// sentinels below match any error carrying that code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Sentinel errors for use with errors.Is. They carry no detail; the error
// actually returned does.
var (
	ErrInvalidCursor   = &Error{Code: CodeInvalidCursor}
	ErrCursorMismatch  = &Error{Code: CodeCursorMismatch}
	ErrInvalidLimit    = &Error{Code: CodeInvalidLimit}
	ErrLimitTooLarge   = &Error{Code: CodeLimitTooLarge}
	ErrUnknownSort     = &Error{Code: CodeUnknownSort}
	ErrInvalidPage     = &Error{Code: CodeInvalidPage}
	ErrConfig          = &Error{Code: CodeConfig}
	ErrUnsortableField = &Error{Code: CodeUnsortableField}
)

func errf(code Code, param, format string, args ...any) *Error {
	return &Error{Code: code, Param: param, msg: fmt.Sprintf(format, args...)}
}

func wrapf(code Code, param string, err error, format string, args ...any) *Error {
	return &Error{Code: code, Param: param, msg: fmt.Sprintf(format, args...), err: err}
}
