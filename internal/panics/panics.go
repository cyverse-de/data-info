// Package panics turns a panic on a background goroutine into an error. Echo's recover
// middleware covers only handler goroutines; a panic anywhere else ends the process, and
// with it every job in flight, whose paths then stay locked until async-tasks times them out.
package panics

import (
	"fmt"
	"runtime/debug"
)

// Error is a recovered panic. Its message is short enough for a response or a task's
// status; the stack is for the log.
type Error struct {
	Value any
	Stack []byte
}

func (e *Error) Error() string { return fmt.Sprintf("panic: %v; this is a bug in data-info", e.Value) }

// Unwrap exposes a panic value that was itself an error.
func (e *Error) Unwrap() error {
	err, _ := e.Value.(error)
	return err
}

// From converts the result of recover into an *Error, or nil if nothing panicked. It must
// be given recover() called directly in a deferred function:
//
//	defer func() {
//		if p := panics.From(recover()); p != nil {
//			err = p
//		}
//	}()
func From(recovered any) *Error {
	if recovered == nil {
		return nil
	}
	return &Error{Value: recovered, Stack: debug.Stack()}
}
