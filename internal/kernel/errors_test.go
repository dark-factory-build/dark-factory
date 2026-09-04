package kernel

import (
	"errors"
	"testing"
)

func TestNewOutcomeUnknownErrorPreservesTypedCause(t *testing.T) {
	cause := errors.New("durable result unavailable")
	err := NewOutcomeUnknownError(cause)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, cause) {
		t.Fatalf("outcome unknown = %v", err)
	}
	if err := NewOutcomeUnknownError(nil); !errors.Is(err, ErrInvalidValue) || errors.As(err, &unknown) {
		t.Fatalf("nil outcome cause = %v", err)
	}
}
