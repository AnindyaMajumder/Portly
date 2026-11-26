//go:build !amd64 && !arm64

package stopper

// platformGates stays closed on Linux processors where no real stop has run, only emulation.
func platformGates() gates { return gates{graceful: true, families: true} }
