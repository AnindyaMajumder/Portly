package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxConfig = 1 << 20

// environment is the caller's own settings, injected for tests.
type environment struct {
	host       string
	context    string
	configDir  string
	home       string
	runtimeDir string
	// Root of absolute socket paths
	root string
}

// callerEnvironment reads only Portly's own environment.
func callerEnvironment() environment {
	home, _ := os.UserHomeDir()
	configDir := os.Getenv("DOCKER_CONFIG")
	if configDir == "" {
		configDir = filepath.Join(home, ".docker")
	}
	return environment{
		host: os.Getenv("DOCKER_HOST"), context: os.Getenv("DOCKER_CONTEXT"), configDir: configDir,
		home: home, runtimeDir: os.Getenv("XDG_RUNTIME_DIR"), root: "/",
	}
}

// localPaths lists candidate sockets in order, without repeats.
func (e environment) localPaths() ([]string, []string) {
	var paths, warnings []string
	if path, isLocal := localPath(e.host); isLocal {
		paths = append(paths, path)
	} else if e.host != "" {
		warnings = append(warnings, "DOCKER_HOST is not a local socket, it is ignored")
	}
	if path, isLocal := localPath(e.contextHost()); isLocal {
		paths = append(paths, path)
	}
	return distinctPaths(append(paths, e.knownPaths()...)), warnings
}

// distinctPaths keeps the first of each path.
func distinctPaths(paths []string) []string {
	seen := map[string]bool{}
	var kept []string
	for _, path := range paths {
		if path != "" && !seen[path] {
			seen[path] = true
			kept = append(kept, path)
		}
	}
	return kept
}

// contextHost reads the endpoint of the chosen Docker context.
func (e environment) contextHost() string {
	name := e.context
	if name == "" {
		var config struct {
			CurrentContext string `json:"currentContext"`
		}
		if json.Unmarshal(readConfig(filepath.Join(e.configDir, "config.json")), &config) != nil {
			return ""
		}
		name = config.CurrentContext
	}
	if name == "" || name == "default" {
		return ""
	}
	digest := sha256.Sum256([]byte(name))
	var meta struct {
		Endpoints map[string]struct{ Host string }
	}
	path := filepath.Join(e.configDir, "contexts", "meta", hex.EncodeToString(digest[:]), "meta.json")
	if json.Unmarshal(readConfig(path), &meta) != nil {
		return ""
	}
	return meta.Endpoints["docker"].Host
}

// readConfig returns a capped regular file, nil when unreadable.
func readConfig(path string) []byte {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfig {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, _ := io.ReadAll(io.LimitReader(file, maxConfig))
	return data
}

// localPath accepts unix sockets and named pipes only.
func localPath(host string) (string, bool) {
	if path, found := strings.CutPrefix(host, "unix://"); found && filepath.IsAbs(path) {
		return filepath.Clean(path), true
	}
	if path, found := strings.CutPrefix(host, "npipe://"); found {
		path = strings.ReplaceAll(path, "/", `\`)
		return path, isLocalPipe(path)
	}
	return "", false
}

// isLocalPipe rejects pipes of other machines.
func isLocalPipe(path string) bool { return strings.HasPrefix(path, `\\.\pipe\`) }
