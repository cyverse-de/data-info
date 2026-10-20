package panics

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrom(t *testing.T) {
	tests := []struct {
		name      string
		panicWith any
		wantIs    error
	}{
		{"a string", "boom", nil},
		{"an error", io.ErrUnexpectedEOF, io.ErrUnexpectedEOF},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := func() (err error) {
				defer func() {
					if p := From(recover()); p != nil {
						err = p
					}
				}()
				panic(tt.panicWith)
			}()

			var p *Error
			if !errors.As(err, &p) {
				t.Fatalf("err = %v, want an *Error", err)
			}
			if !strings.Contains(p.Error(), "panic: ") || len(p.Stack) == 0 {
				t.Errorf("Error() = %q, stack %d bytes; want the value and a stack", p.Error(), len(p.Stack))
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false", tt.wantIs)
			}
		})
	}

	if From(nil) != nil {
		t.Error("From(nil) reported a panic where there was none")
	}
}
