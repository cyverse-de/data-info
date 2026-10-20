package apierror

import "testing"

func TestIsServerFault(t *testing.T) {
	faults := map[Code]bool{
		ErrConfigInvalid:      true,
		ErrRequestFailed:      true,
		ErrUnavailable:        true,
		ErrUncheckedException: true,
	}

	for _, code := range append(EmittedCodes(), ErrUncheckedException) {
		t.Run(string(code), func(t *testing.T) {
			if got := code.IsServerFault(); got != faults[code] {
				t.Errorf("IsServerFault() = %v, want %v", got, faults[code])
			}
		})
	}
}
