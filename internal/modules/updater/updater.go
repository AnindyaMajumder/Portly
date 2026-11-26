// Package updater replaces this binary with a newer, verified release from GitHub.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const (
	repository = "https://github.com/AnindyaMajumder/Portly"
	// A release archive is about 5 MiB
	maxArchive     = 64 << 20
	maxChecksums   = 1 << 20
	requestTimeout = 2 * time.Minute
	maxRedirects   = 5
)

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	latestTag      = regexp.MustCompile(`/releases/tag/v([0-9]+\.[0-9]+\.[0-9]+)$`)
)

// Updater finds, verifies and installs releases.
type Updater struct {
	base   string
	client *http.Client
	// Like linux_amd64 or windows_arm64, empty if unknown
	platform   string
	executable string
}

// New returns an updater for the running binary.
func New() *Updater {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		executable = ""
	}
	client := &http.Client{Timeout: requestTimeout, CheckRedirect: httpsOnly}
	return &Updater{base: repository, client: client, platform: buildPlatform(), executable: executable}
}

// httpsOnly follows a few redirects, never to plain HTTP.
func httpsOnly(request *http.Request, via []*http.Request) error {
	if request.URL.Scheme != "https" || len(via) >= maxRedirects {
		return errors.New("unsafe redirect")
	}
	return nil
}

// buildPlatform names this binary's release archive, from what the Go toolchain recorded.
func buildPlatform() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	system, arch := settings["GOOS"], settings["GOARCH"]
	if system == "" || arch == "" {
		return ""
	}
	if arch == "arm" {
		arch += "v" + strings.SplitN(settings["GOARM"], ",", 2)[0]
	}
	return system + "_" + arch
}

// Latest is the newest release's version, read from GitHub's redirect.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, u.base+"/releases/latest", nil)
	if err != nil {
		return "", fmt.Errorf("ask for the latest release: %w", err)
	}
	client := *u.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return "", unavailable(ctx, err)
	}
	_ = response.Body.Close()
	match := latestTag.FindStringSubmatch(response.Header.Get("Location"))
	if match == nil {
		return "", domain.NewError(domain.CodeUpdateUnavailable, "Could not find the latest release",
			"Check your connection and try again, or see "+repository+"/releases")
	}
	return match[1], nil
}

// Target is the running binary's path, if Portly may replace it.
func (u *Updater) Target() (string, error) {
	if u.executable == "" {
		return "", domain.NewError(domain.CodeUpdateUnavailable, "Could not find where portly is installed",
			"Install the new version with the install script from "+repository)
	}
	dir := filepath.Dir(u.executable)
	if isPackaged(dir) {
		return "", domain.NewError(domain.CodePackageManaged, "A package manager installed portly in "+dir,
			"Download the new package from "+repository+"/releases/latest and install it the same way")
	}
	probe, err := os.CreateTemp(dir, ".portly-update-*")
	if err != nil {
		return "", domain.NewError(domain.CodePermissionDenied, "Can't write to "+dir, "Run the update as the owner of "+dir)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return u.executable, nil
}

// Install downloads a release, verifies it and replaces the binary at path.
func (u *Updater) Install(ctx context.Context, version, path string) error {
	if !versionPattern.MatchString(version) || u.platform == "" {
		return domain.NewError(domain.CodeUpdateUnavailable, "No release matches this build", "Install the new version with the install script from "+repository)
	}
	archive := "portly_" + version + "_" + u.platform + ".tar.gz"
	if strings.HasPrefix(u.platform, "windows_") {
		archive = strings.TrimSuffix(archive, ".tar.gz") + ".zip"
	}
	sums, err := u.download(ctx, version, "checksums.txt", maxChecksums)
	if err != nil {
		return err
	}
	want, found := checksumFor(sums, archive)
	if !found {
		return domain.NewError(domain.CodeUpdateUnavailable, "Release "+version+" has no build for "+u.platform, "See "+repository+"/releases for what is available")
	}
	data, err := u.download(ctx, version, archive, maxArchive)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
		return domain.NewError(domain.CodeChecksumMismatch, "The download does not match its checksum, nothing was changed",
			"Try again. If it keeps failing, report it at "+repository+"/issues")
	}
	binary, err := extract(archive, data)
	if err != nil {
		return err
	}
	return replace(path, binary)
}

// download fetches one release file, refusing anything larger than limit.
func (u *Updater) download(ctx context.Context, version, name string, limit int64) ([]byte, error) {
	address := u.base + "/releases/download/v" + version + "/" + name
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	response, err := u.client.Do(request)
	if err != nil {
		return nil, unavailable(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, unavailable(ctx, fmt.Errorf("%s: %s", name, response.Status))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, unavailable(ctx, err)
	}
	if int64(len(data)) > limit {
		return nil, domain.NewError(domain.CodeUpdateUnavailable, name+" is larger than expected, nothing was changed", "Report it at "+repository+"/issues")
	}
	return data, nil
}

// unavailable explains a failed download, keeping Ctrl-C a cancellation.
func unavailable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return domain.NewError(domain.CodeUpdateUnavailable, "Could not download the update: "+err.Error(), "Check your connection and try again")
}

// checksumFor finds one file's SHA-256 in a checksums.txt.
func checksumFor(sums []byte, name string) (string, bool) {
	for line := range strings.Lines(string(sums)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name && len(fields[0]) == sha256.Size*2 {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// replace writes the new binary next to the old one, then swaps them.
func replace(path string, binary []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".portly-update-*")
	if err != nil {
		return domain.NewError(domain.CodePermissionDenied, "Can't write to "+filepath.Dir(path), "Run the update as the owner of that folder")
	}
	// Gone already after a successful swap
	defer func() { _ = os.Remove(temporary.Name()) }()
	_, err = temporary.Write(binary)
	if err == nil {
		err = temporary.Chmod(0o755)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write the new binary, nothing was changed: %w", err)
	}
	if err := swap(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
