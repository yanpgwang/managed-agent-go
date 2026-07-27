package domain

import "testing"

func TestErrInvalidTransitionSentinel(t *testing.T) {
	if ErrInvalidTransition == nil {
		t.Fatal("ErrInvalidTransition must not be nil")
	}
	if ErrInvalidTransition.Kind != KindValidation {
		t.Errorf("expected Kind %d (KindValidation), got %d", KindValidation, ErrInvalidTransition.Kind)
	}
}
