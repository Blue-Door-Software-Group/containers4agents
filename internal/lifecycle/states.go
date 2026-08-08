package lifecycle

const (
	StateCreating = "creating"
	StateRunning  = "running"
	StateStopping = "stopping"
	StateStopped  = "stopped"
	StateStarting = "starting"
	StatePruning  = "pruning"
	StatePruned   = "pruned"
	StateFailed   = "failed"
)

func IsKnownState(state string) bool {
	switch state {
	case StateCreating, StateRunning, StateStopping, StateStopped, StateStarting, StatePruning, StatePruned, StateFailed:
		return true
	default:
		return false
	}
}
