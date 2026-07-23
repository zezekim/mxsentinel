package relayfailover

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteDomains atomically writes the set of recipient domains currently in failover (one
// per line, sorted) to path. The host-side hook reads this file and rebuilds the Postfix
// transport overlay + requeues deferred mail. relayfailoverd never touches host Postfix.
//
// An EMPTY set writes an empty file (NOT a missing file): the hook treats an empty file as
// "no domains in failover — clear the overlay", which is the correct closed-breaker state.
// The write is atomic (temp + rename) so a concurrent reader never sees a partial file.
func WriteDomains(path string, domains []string) error {
	if path == "" {
		return fmt.Errorf("failover state-file path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	sorted := append([]string(nil), domains...)
	sort.Strings(sorted)
	var b strings.Builder
	for _, d := range sorted {
		if t := strings.TrimSpace(d); t != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
	}

	tmp, err := os.CreateTemp(dir, ".failover-domains-*.tmp")
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
