package lifecycle

import "fmt"

type TransitionOp string

const (
	TransitionCreateSucceeded TransitionOp = "create_succeeded"
	TransitionCreateFailed    TransitionOp = "create_failed"
	TransitionStopRequested   TransitionOp = "stop_requested"
	TransitionStopCompleted   TransitionOp = "stop_completed"
	TransitionStartRequested  TransitionOp = "start_requested"
	TransitionStartCompleted  TransitionOp = "start_completed"
	TransitionStartFailed     TransitionOp = "start_failed"
	TransitionPruneRequested  TransitionOp = "prune_requested"
	TransitionPruneCompleted  TransitionOp = "prune_completed"
	TransitionPruneFailed     TransitionOp = "prune_failed"
)

func NextState(current string, op TransitionOp) (string, error) {
	switch op {
	case TransitionCreateSucceeded:
		if current != StateCreating {
			return "", conflictf(current, op)
		}
		return StateRunning, nil
	case TransitionCreateFailed:
		if current != StateCreating {
			return "", conflictf(current, op)
		}
		return StateFailed, nil
	case TransitionStopRequested:
		if current != StateRunning {
			return "", conflictf(current, op)
		}
		return StateStopping, nil
	case TransitionStopCompleted:
		if current != StateStopping {
			return "", conflictf(current, op)
		}
		return StateStopped, nil
	case TransitionStartRequested:
		if current != StateStopped {
			return "", conflictf(current, op)
		}
		return StateStarting, nil
	case TransitionStartCompleted:
		if current != StateStarting {
			return "", conflictf(current, op)
		}
		return StateRunning, nil
	case TransitionStartFailed:
		if current != StateStarting {
			return "", conflictf(current, op)
		}
		return StateFailed, nil
	case TransitionPruneRequested:
		if current != StateRunning && current != StateStopped {
			return "", conflictf(current, op)
		}
		return StatePruning, nil
	case TransitionPruneCompleted:
		if current != StatePruning {
			return "", conflictf(current, op)
		}
		return StatePruned, nil
	case TransitionPruneFailed:
		if current != StatePruning {
			return "", conflictf(current, op)
		}
		return StateFailed, nil
	default:
		return "", fmt.Errorf("%w: unknown transition op %q", ErrStateConflict, op)
	}
}

func conflictf(current string, op TransitionOp) error {
	return fmt.Errorf("%w: state=%s op=%s", ErrStateConflict, current, op)
}
