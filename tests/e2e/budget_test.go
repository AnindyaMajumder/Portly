//go:build linux

package e2e

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const (
	timeBudget   = 900 * time.Millisecond
	memoryBudget = 30 << 10 // KiB
	budgetRuns   = 10
)

// TestBudget measures the list on this machine.
func TestBudget(t *testing.T) {
	if os.Getenv("PORTLY_PERF_TESTS") == "" {
		t.Skip("set PORTLY_PERF_TESTS=1 to measure")
	}
	var slowest time.Duration
	var largest int64
	for range budgetRuns {
		command := exec.Command(release, "--json")
		started := time.Now()
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
		slowest = max(slowest, time.Since(started))
		largest = max(largest, int64(command.ProcessState.SysUsage().(*syscall.Rusage).Maxrss))
	}
	info, err := os.Stat(release)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("slowest of %d: %v, peak memory %.1f MiB, unstripped binary %.1f MiB", budgetRuns, slowest, float64(largest)/1024, float64(info.Size())/(1<<20))
	if slowest > timeBudget || largest > memoryBudget {
		t.Error("over budget")
	}
}
