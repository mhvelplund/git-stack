// Package exitcode defines process exit codes, matching gh-stack.
package exitcode

import (
	"errors"
	"fmt"

	"gitlab.com/malephex/git-stack/pkg/protocol"
)

const (
	OK               = 0
	Generic          = 1
	NotInStack       = 2
	RebaseConflict   = 3
	ProviderFailure  = 4 // the Git host (through its provider) failed
	InvalidArgs      = 5
	Ambiguous        = 6
	RebaseInProgress = 7
	Locked           = 8
	Unsupported      = 9 // the provider cannot do this, e.g. provider "none" and pull requests
)

// Error carries an exit code alongside an error.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// New returns an error that exits with code.
func New(code int, format string, args ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)}
}

// Wrap attaches code to err. A nil err stays nil.
func Wrap(code int, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// Of returns the exit code for err.
func Of(err error) int {
	if err == nil {
		return OK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		if pe.Code == protocol.CodeUnsupported {
			return Unsupported
		}
		return ProviderFailure
	}
	return Generic
}
