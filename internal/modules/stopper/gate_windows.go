package stopper

// platformGates stays closed, and Windows has no graceful method.
func platformGates() gates { return gates{} }
