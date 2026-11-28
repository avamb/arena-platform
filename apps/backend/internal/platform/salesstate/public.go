package salesstate

// Public is the state of a session as an API reader sees it: the internal
// ControlOnly state (a session that exists for door control only, migration
// 0228, MA-40) is reported as Ended, because the API enum has no value of its
// own for it. Every other state is returned as is. Every place that
// serializes an Effective state for a client goes through it.
func Public(effective string) string {
	if effective == ControlOnly {
		return Ended
	}
	return effective
}
