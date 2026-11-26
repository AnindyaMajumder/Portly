//go:build amd64 || arm64

package stopper

// platformGates is open: the Linux evidence passed on amd64 and arm64 in CI run 45880755420 (user's OK, 2026-11-27).
func platformGates() gates { return gates{native: true, docker: true, graceful: true, families: true} }
