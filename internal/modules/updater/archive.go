package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// extract returns the one portly binary in a release archive and ignores everything else.
func extract(archive string, data []byte) ([]byte, error) {
	var binary []byte
	var err error
	if strings.HasSuffix(archive, ".zip") {
		binary, err = fromZip(data)
	} else {
		binary, err = fromTarGz(data)
	}
	if err != nil || binary == nil {
		return nil, domain.NewError(domain.CodeUpdateUnavailable, "The release archive has no usable portly binary, nothing was changed",
			"Report it at "+repository+"/issues")
	}
	return binary, nil
}

// fromTarGz reads the regular file named portly at the top of a tar.gz.
func fromTarGz(data []byte) ([]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	entries := tar.NewReader(compressed)
	for {
		header, err := entries.Next()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if header.Name == "portly" && header.Typeflag == tar.TypeReg {
			return readLimited(entries)
		}
	}
}

// fromZip reads the regular file named portly.exe at the top of a zip.
func fromZip(data []byte) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, file := range archive.File {
		if file.Name != "portly.exe" || !file.Mode().IsRegular() {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = entry.Close() }()
		return readLimited(entry)
	}
	return nil, nil
}

// readLimited refuses a binary that unpacks larger than an archive may be.
func readLimited(reader io.Reader) ([]byte, error) {
	binary, err := io.ReadAll(io.LimitReader(reader, maxArchive+1))
	if err != nil {
		return nil, err
	}
	if len(binary) > maxArchive || len(binary) == 0 {
		return nil, errors.New("binary has an unexpected size")
	}
	return binary, nil
}
