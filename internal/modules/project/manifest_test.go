package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestNames covers every format and odd content.
func TestManifestNames(t *testing.T) {
	cases := []struct{ marker, data, want string }{
		{"go.mod", "// x\nmodule github.com/acme/tool\n", "tool"},
		{"go.mod", "module github.com/acme/tool/v2\n", "tool"},
		{"go.mod", "module \"quoted\"\n", "quoted"},
		{"go.mod", "go 1.27\n", ""},
		{"package.json", `{"name":"@acme/web"}`, "@acme/web"},
		{"package.json", `{"name":5}`, ""},
		{"package.json", `[]`, ""},
		{"composer.json", `{"name":"acme/site"}`, "acme/site"},
		{"pyproject.toml", "[build-system]\nname = \"no\"\n[project]\nname='api' # comment\n", "api"},
		{"pyproject.toml", "[tool.poetry]\nname = \"old\"\n", "old"},
		{"pyproject.toml", "name = \"top\"\n", ""},
		{"Cargo.toml", "[package]\nname = \"crate\"\n[[bin]]\nname = \"other\"\n", "crate"},
		{"Cargo.toml", "[[bin]]\nname = \"other\"\n", ""},
		{"Cargo.toml", "[package]\nname.workspace = true\n", ""},
		{"package.json", `{"name":"` + strings.Repeat("x", 101) + `"}`, ""},
		{"pom.xml", "<name>x</name>", ""},
	}
	for _, c := range cases {
		if got := manifestName(c.marker, []byte(c.data)); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.marker, c.data, got, c.want)
		}
	}
}

// TestManifestIsReadSafely refuses links, directories and big files.
func TestManifestIsReadSafely(t *testing.T) {
	dir := tree(t, map[string]string{"ok": "data", "secret": "secret", "sub/file": ""})
	if err := os.WriteFile(filepath.Join(dir, "big"), make([]byte, maxManifest+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("secret", filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	if got := string(readManifest(dir, "ok")); got != "data" {
		t.Errorf("got %q", got)
	}
	for _, name := range []string{"link", "big", "sub", "missing", "../" + filepath.Base(dir) + "/ok"} {
		if got := readManifest(dir, name); got != nil {
			t.Errorf("%s was read", name)
		}
	}
	if got := readManifest(filepath.Join(dir, "gone"), "ok"); got != nil {
		t.Error("a missing directory was read")
	}
}

// TestSymlinkIsNotAMarker keeps hostile links from naming a project.
func TestSymlinkIsNotAMarker(t *testing.T) {
	dir := tree(t, map[string]string{"code/real.json": "{}"})
	if err := os.Symlink("real.json", filepath.Join(dir, "code/package.json")); err != nil {
		t.Skip(err)
	}
	if got := markerIn(filepath.Join(dir, "code")); got != "" {
		t.Errorf("got %q", got)
	}
}

// FuzzManifests proves no parser panics or returns a long name.
func FuzzManifests(f *testing.F) {
	f.Add("[project]\nname = \"x\"\n")
	f.Add("module a/b/v2\n")
	f.Add(`{"name":"x"}`)
	f.Fuzz(func(t *testing.T, text string) {
		for _, marker := range markers {
			if len(manifestName(marker, []byte(text))) > maxName {
				t.Fatal("name is too long")
			}
		}
	})
}
