package stopper

// platformGates stays closed until the Linux evidence gate has passed.
func platformGates() gates { return gates{graceful: true, families: true} }
