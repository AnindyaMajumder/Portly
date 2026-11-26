package app

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain runs the tests, then fails if any of them left a goroutine behind.
func TestMain(m *testing.M) {
	code := m.Run()
	if leaked := leakedGoroutines(); code == 0 && leaked != "" {
		fmt.Fprintln(os.Stderr, "leaked goroutines:\n"+leaked)
		code = 1
	}
	os.Exit(code)
}

// leakedGoroutines waits up to 2 s for Portly goroutines other than this one to end.
func leakedGoroutines() string {
	deadline := time.Now().Add(2 * time.Second)
	for {
		buffer := make([]byte, 1<<20)
		stacks := strings.Split(string(buffer[:runtime.Stack(buffer, true)]), "\n\n")
		var leaked []string
		for _, stack := range stacks[1:] {
			if strings.Contains(stack, "Portly/internal/") {
				leaked = append(leaked, stack)
			}
		}
		if len(leaked) == 0 || time.Now().After(deadline) {
			return strings.Join(leaked, "\n\n")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
