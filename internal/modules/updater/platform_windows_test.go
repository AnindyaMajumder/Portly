package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSwapKeepsThePreviousBinaryAside replaces twice, as two updates in a row would.
func TestSwapKeepsThePreviousBinaryAside(t *testing.T) {
	dir := t.TempDir()
	path, next := filepath.Join(dir, "portly.exe"), filepath.Join(dir, "next.exe")
	for _, version := range []string{"one", "two"} {
		if err := os.WriteFile(path, []byte("old "+version), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(next, []byte("new "+version), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := swap(next, path); err != nil {
			t.Fatal(err)
		}
		current, _ := os.ReadFile(path)
		previous, _ := os.ReadFile(path + ".old")
		if string(current) != "new "+version || string(previous) != "old "+version {
			t.Errorf("%s: %q, previous %q", version, current, previous)
		}
	}
}

// TestFailedSwapPutsTheBinaryBack restores the old binary when the new one is missing.
func TestFailedSwapPutsTheBinaryBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "portly.exe")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swap(filepath.Join(dir, "missing.exe"), path); err == nil {
		t.Fatal("swap of a missing file succeeded")
	}
	if current, _ := os.ReadFile(path); string(current) != "old" {
		t.Errorf("got %q", current)
	}
}
