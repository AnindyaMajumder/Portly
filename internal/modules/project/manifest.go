package project

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"
)

const (
	maxManifest = 1 << 20
	maxName     = 100
)

// readManifest opens inside the root only. Links, FIFOs and big files give nothing.
func readManifest(directory, name string) []byte {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return nil
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() > maxManifest || !os.SameFile(before, opened) {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxManifest+1))
	if err != nil || len(data) > maxManifest {
		return nil
	}
	return data
}

// manifestName extracts the declared name, empty when there is none.
func manifestName(marker string, data []byte) string {
	var name string
	switch marker {
	case "go.mod":
		name = goModuleName(data)
	case "package.json", "composer.json", "deno.json":
		name = jsonName(data)
	case "pyproject.toml":
		if name = tomlName(data, "project"); name == "" {
			name = tomlName(data, "tool.poetry")
		}
	case "Cargo.toml":
		name = tomlName(data, "package")
	}
	if name = strings.TrimSpace(name); len(name) > maxName {
		return ""
	}
	return name
}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

// goModuleName is the last path element, without a major version.
func goModuleName(data []byte) string {
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "module" {
			continue
		}
		module := strings.Trim(fields[1], `"`)
		if base := path.Base(module); !majorVersion.MatchString(base) {
			return base
		}
		return path.Base(path.Dir(module))
	}
	return ""
}

// jsonName reads a top-level name.
func jsonName(data []byte) string {
	var manifest struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return manifest.Name
}

var tomlString = regexp.MustCompile(`^name\s*=\s*(?:"([^"\\]*)"|'([^']*)')\s*(?:#.*)?$`)

// tomlName reads name from one section. Anything unusual gives nothing.
func tomlName(data []byte, section string) string {
	current := ""
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			current = strings.Trim(line, "[] ")
			continue
		}
		if match := tomlString.FindStringSubmatch(line); match != nil && current == section {
			return match[1] + match[2]
		}
	}
	return ""
}
