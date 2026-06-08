package rbl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteHealthyIPs atomically writes the healthy-IP set (one IP per line) to path. A
// host-side hook (cron/script) reads this file and rebuilds the Postfix randmap source,
// then runs `postfix reload` — rbld itself never touches host Postfix. See install_notes.
//
// The write is atomic (temp file + rename) so a concurrent reader never sees a partial
// file. Parent directories are created if missing. An empty set produces an empty file
// (NOT a missing file), which the host hook should treat as "no healthy IPs — do not narrow
// the pool to zero" (a safety check the hook must enforce; documented in install_notes).
func WriteHealthyIPs(path string, ips []string) error {
	if path == "" {
		return fmt.Errorf("healthy-ips path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	var b strings.Builder
	for _, ip := range ips {
		b.WriteString(ip)
		b.WriteByte('\n')
	}

	tmp, err := os.CreateTemp(dir, ".healthy-ips-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	return nil
}
