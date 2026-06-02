package dmarc

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
)

// Decompress decompresses data based on the file extension in filename.
// - ".gz"  → gzip decompress
// - ".zip" → extract the first entry whose name ends with ".xml"
// - other  → return data unchanged
// Returns an error on decompression failure or if a zip has no .xml entry.
func Decompress(filename string, data []byte) ([]byte, error) {
	lower := strings.ToLower(filename)

	switch {
	case strings.HasSuffix(lower, ".gz"):
		return decompressGzip(data)
	case strings.HasSuffix(lower, ".zip"):
		return decompressZip(data)
	default:
		return data, nil
	}
}

func decompressGzip(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("dmarc: gzip open: %w", err)
	}
	defer gr.Close()

	out, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("dmarc: gzip read: %w", err)
	}
	return out, nil
}

func decompressZip(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("dmarc: zip open: %w", err)
	}

	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("dmarc: zip entry open %q: %w", f.Name, err)
			}
			defer rc.Close()

			out, err := io.ReadAll(rc)
			if err != nil {
				return nil, fmt.Errorf("dmarc: zip entry read %q: %w", f.Name, err)
			}
			return out, nil
		}
	}

	return nil, fmt.Errorf("dmarc: zip contains no .xml entry")
}
