//go:build unix

package project

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestFIFODoesNotBlock proves a planted pipe can't hang a scan.
func TestFIFODoesNotBlock(t *testing.T) {
	dir := tree(t, map[string]string{"code/x": ""})
	if err := syscall.Mkfifo(filepath.Join(dir, "code/package.json"), 0o644); err != nil {
		t.Skip(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if got := readManifest(filepath.Join(dir, "code"), "package.json"); got != nil || markerIn(filepath.Join(dir, "code")) != "" {
			t.Error("a FIFO was used")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}
