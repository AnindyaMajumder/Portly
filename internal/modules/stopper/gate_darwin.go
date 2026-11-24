package stopper

// platformGates stays closed until the macOS evidence gate has passed.
func platformGates() gates { return gates{graceful: true} }
