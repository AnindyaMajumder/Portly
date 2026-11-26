package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// entry is one file in a fake release archive.
type entry struct {
	name     string
	kind     byte
	contents string
}

// tarGz builds a release archive.
func tarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, e := range entries {
		header := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: 0o755, Size: int64(len(e.contents))}
		if e.kind == tar.TypeSymlink {
			header.Linkname, header.Size = e.contents, 0
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if e.kind == tar.TypeReg {
			_, _ = archive.Write([]byte(e.contents))
		}
	}
	if err := errors.Join(archive.Close(), compressed.Close()); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// release serves one fake GitHub release over HTTPS.
type release struct {
	latest  string
	archive []byte
	sums    string
}

// serve starts the fake release and an updater that uses it.
func (r release) serve(t *testing.T) *Updater {
	t.Helper()
	const name = "portly_0.9.1_linux_amd64.tar.gz"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", r.latest)
			w.WriteHeader(http.StatusFound)
		case "/releases/download/v0.9.1/checksums.txt":
			_, _ = w.Write([]byte(r.sums))
		case "/releases/download/v0.9.1/" + name:
			_, _ = w.Write(r.archive)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.CheckRedirect = httpsOnly
	return &Updater{base: server.URL, client: client, platform: func() string { return "linux_amd64" }}
}

// goodRelease has a matching checksum for its archive.
func goodRelease(t *testing.T, entries ...entry) release {
	archive := tarGz(t, entries...)
	sum := sha256.Sum256(archive)
	sums := "0000  other.zip\n" + hex.EncodeToString(sum[:]) + "  portly_0.9.1_linux_amd64.tar.gz\n"
	return release{latest: "https://github.com/AnindyaMajumder/Portly/releases/tag/v0.9.1", archive: archive, sums: sums}
}

// installed writes an old binary to replace.
func installed(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "portly")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// codeOf is the reason code of an error, or empty.
func codeOf(err error) domain.Code {
	var known *domain.Error
	if errors.As(err, &known) {
		return known.Code
	}
	return ""
}

// TestLatestReadsTheRedirect accepts only a release tag.
func TestLatestReadsTheRedirect(t *testing.T) {
	for location, want := range map[string]string{
		"https://github.com/AnindyaMajumder/Portly/releases/tag/v0.9.1": "0.9.1",
		"/AnindyaMajumder/Portly/releases/tag/v1.10.0":                  "1.10.0",
		"https://github.com/AnindyaMajumder/Portly/releases":            "",
		"https://github.com/x/releases/tag/v1.0.0-rc1":                  "",
		"https://github.com/x/releases/tag/v1.0.0/../../evil":           "",
	} {
		got, err := release{latest: location}.serve(t).Latest(t.Context())
		if got != want || (want == "") != (codeOf(err) == domain.CodeUpdateUnavailable) {
			t.Errorf("%s: %q %v", location, got, err)
		}
	}
}

// TestInstallReplacesTheBinary covers the success and every refusal, which change nothing.
func TestInstallReplacesTheBinary(t *testing.T) {
	binary := entry{"portly", tar.TypeReg, "new"}
	badSum := goodRelease(t, binary)
	badSum.sums = strings.Repeat("f", 64) + "  portly_0.9.1_linux_amd64.tar.gz\n"
	noSum := goodRelease(t, binary)
	noSum.sums = "0000  other.zip\n"
	cases := []struct {
		name    string
		release release
		want    domain.Code
	}{
		{"good", goodRelease(t, entry{"README.md", tar.TypeReg, "x"}, binary), ""},
		{"bad checksum", badSum, domain.CodeChecksumMismatch},
		{"no checksum", noSum, domain.CodeUpdateUnavailable},
		{"link", goodRelease(t, entry{"portly", tar.TypeSymlink, "/bin/sh"}), domain.CodeUpdateUnavailable},
		{"path", goodRelease(t, entry{"../portly", tar.TypeReg, "new"}), domain.CodeUpdateUnavailable},
		{"nested", goodRelease(t, entry{"dir/portly", tar.TypeReg, "new"}), domain.CodeUpdateUnavailable},
		{"empty", goodRelease(t, entry{"portly", tar.TypeReg, ""}), domain.CodeUpdateUnavailable},
		{"missing", goodRelease(t, entry{"README.md", tar.TypeReg, "x"}), domain.CodeUpdateUnavailable},
	}
	for _, c := range cases {
		path := installed(t)
		err := c.release.serve(t).Install(t.Context(), "0.9.1", path)
		got, _ := os.ReadFile(path)
		if wantBody := map[bool]string{true: "new", false: "old"}[c.want == ""]; codeOf(err) != c.want || string(got) != wantBody {
			t.Errorf("%s: %v, file %q", c.name, err, got)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".portly-update-*")); len(leftovers) > 0 {
			t.Errorf("%s: left %v", c.name, leftovers)
		}
	}
}

// TestInstallRefusesOddVersions never builds a URL from an unchecked version.
func TestInstallRefusesOddVersions(t *testing.T) {
	updater := goodRelease(t, entry{"portly", tar.TypeReg, "new"}).serve(t)
	for _, version := range []string{"", "0.9", "0.9.1/../../x", "v0.9.1"} {
		if err := updater.Install(t.Context(), version, installed(t)); codeOf(err) != domain.CodeUpdateUnavailable {
			t.Errorf("%q: %v", version, err)
		}
	}
}

// TestDownloadRefusesLargeFiles stops reading at the limit.
func TestDownloadRefusesLargeFiles(t *testing.T) {
	updater := goodRelease(t, entry{"portly", tar.TypeReg, strings.Repeat("x", 4096)}).serve(t)
	if _, err := updater.download(t.Context(), "0.9.1", "portly_0.9.1_linux_amd64.tar.gz", 10); codeOf(err) != domain.CodeUpdateUnavailable {
		t.Errorf("got %v", err)
	}
	if _, err := updater.download(t.Context(), "0.9.1", "missing.tar.gz", 10); codeOf(err) != domain.CodeUpdateUnavailable {
		t.Errorf("missing file: %v", err)
	}
}

// TestCancelledDownloadIsACancellation keeps Ctrl-C distinct from a network failure.
func TestCancelledDownloadIsACancellation(t *testing.T) {
	updater := goodRelease(t, entry{"portly", tar.TypeReg, "new"}).serve(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := updater.Latest(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

// TestZipTakesOnlyTheExecutable ignores nested and other entries.
func TestZipTakesOnlyTheExecutable(t *testing.T) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, contents := range map[string]string{"sub/portly.exe": "nested", "portly.exe": "new", "LICENSE": "x"} {
		writer, _ := archive.Create(name)
		_, _ = writer.Write([]byte(contents))
	}
	_ = archive.Close()
	got, err := extract("portly_0.9.1_windows_amd64.zip", buffer.Bytes())
	if err != nil || string(got) != "new" {
		t.Errorf("%q %v", got, err)
	}
	if _, err := extract("portly.zip", []byte("not a zip")); codeOf(err) != domain.CodeUpdateUnavailable {
		t.Errorf("broken zip: %v", err)
	}
}

// TestInstallPathNeedsAKnownWritableFolder refuses before anything is downloaded.
func TestInstallPathNeedsAKnownWritableFolder(t *testing.T) {
	path := installed(t)
	found := &Updater{locate: func() (string, error) { return path, nil }}
	if got, err := found.InstallPath(); got != path || err != nil {
		t.Errorf("writable: %q %v", got, err)
	}
	lost := &Updater{locate: func() (string, error) { return "", os.ErrNotExist }}
	if _, err := lost.InstallPath(); codeOf(err) != domain.CodeUpdateUnavailable {
		t.Errorf("unknown path: %v", err)
	}
	gone := &Updater{locate: func() (string, error) { return filepath.Join(path, "missing", "portly"), nil }}
	if _, err := gone.InstallPath(); codeOf(err) != domain.CodeUpdateUnavailable {
		t.Errorf("missing folder is not a permission problem: %v", err)
	}
}

// TestChecksumFor needs the exact name and a full hash.
func TestChecksumFor(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	sums := []byte(hash + "  portly_1.0.0_linux_amd64.tar.gz.sbom.json\n" + "abc  portly.tar.gz\n" + strings.ToUpper(hash) + "  portly.tar.gz\n")
	if got, ok := checksumFor(sums, "portly.tar.gz"); !ok || got != hash {
		t.Errorf("%q %v", got, ok)
	}
	if _, ok := checksumFor(sums, "portly_1.0.0_linux_amd64.tar.gz"); ok {
		t.Error("a longer name matched")
	}
}

// TestBuildPlatform names an archive for this test binary.
func TestBuildPlatform(t *testing.T) {
	if got := buildPlatform(); !strings.Contains(got, "_") {
		t.Errorf("got %q", got)
	}
}

// FuzzDownloads never panics on hostile checksums or archives, and extracts only a sane binary.
func FuzzDownloads(f *testing.F) {
	t := &testing.T{}
	f.Add([]byte(strings.Repeat("ab", 32)+"  portly.tar.gz\n"), tarGz(t, entry{"portly", tar.TypeReg, "new"}))
	f.Add([]byte("x"), []byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, sums, archive []byte) {
		if hash, ok := checksumFor(sums, "portly.tar.gz"); ok && len(hash) != 64 {
			t.Errorf("hash %q", hash)
		}
		for _, name := range []string{"portly.tar.gz", "portly.zip"} {
			if binary, err := extract(name, archive); err == nil && (len(binary) == 0 || len(binary) > maxArchive) {
				t.Errorf("%s: %d bytes", name, len(binary))
			}
		}
		_ = domain.IsNewerVersion(string(sums), "0.8.0")
	})
}
