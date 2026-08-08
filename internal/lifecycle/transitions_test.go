package lifecycle

import "testing"

func TestNextStateValidTransitions(t *testing.T) {
	tests := []struct {
		current string
		op      TransitionOp
		next    string
	}{
		{StateCreating, TransitionCreateSucceeded, StateRunning},
		{StateCreating, TransitionCreateFailed, StateFailed},
		{StateRunning, TransitionStopRequested, StateStopping},
		{StateStopping, TransitionStopCompleted, StateStopped},
		{StateStopped, TransitionStartRequested, StateStarting},
		{StateStarting, TransitionStartCompleted, StateRunning},
		{StateStarting, TransitionStartFailed, StateFailed},
		{StateRunning, TransitionPruneRequested, StatePruning},
		{StateStopped, TransitionPruneRequested, StatePruning},
		{StatePruning, TransitionPruneCompleted, StatePruned},
		{StatePruning, TransitionPruneFailed, StateFailed},
	}

	for _, tt := range tests {
		got, err := NextState(tt.current, tt.op)
		if err != nil {
			t.Fatalf("expected no error for %s + %s, got %v", tt.current, tt.op, err)
		}
		if got != tt.next {
			t.Fatalf("expected next state %s, got %s", tt.next, got)
		}
	}
}

func TestNextStateInvalidTransitionsConflict(t *testing.T) {
	tests := []struct {
		current string
		op      TransitionOp
	}{
		{StateStopped, TransitionStopRequested},
		{StateRunning, TransitionStartRequested},
		{StatePruned, TransitionStartRequested},
		{StatePruned, TransitionPruneRequested},
	}

	for _, tt := range tests {
		if _, err := NextState(tt.current, tt.op); err == nil {
			t.Fatalf("expected error for %s + %s", tt.current, tt.op)
		}
	}
}
