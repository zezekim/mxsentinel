package telemetry

import (
	"bytes"
	"fmt"
	"os"
	"sync"
)

// Spool is a newline-delimited on-disk buffer of marshaled envelopes. When the event bus
// is unavailable, telemetryd appends events here and replays them on recovery, so relay
// mail flow never depends on MX Sentinel being up (docs/phase-1-plan.md WS4 guardrail).
//
// Each entry is one line of JSON (envelopes are single-line, no embedded newlines).
type Spool struct {
	path string
	mu   sync.Mutex
}

// NewSpool returns a spool backed by the file at path (created on first append).
func NewSpool(path string) *Spool { return &Spool{path: path} }

// Append adds one marshaled envelope to the spool.
func (s *Spool) Append(raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open spool: %w", err)
	}
	defer f.Close()
	line := append(bytes.TrimRight(raw, "\n"), '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write spool: %w", err)
	}
	return nil
}

// Drain replays spooled entries through fn. Entries fn handles successfully are removed;
// entries that fail are kept for the next drain. Returns how many were drained and how
// many remain.
func (s *Spool) Drain(fn func([]byte) error) (drained, remaining int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read spool: %w", err)
	}

	var kept [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if ferr := fn(line); ferr != nil {
			kept = append(kept, line)
		} else {
			drained++
		}
	}
	remaining = len(kept)

	if remaining == 0 {
		return drained, 0, os.Remove(s.path)
	}
	out := bytes.Join(kept, []byte("\n"))
	out = append(out, '\n')
	if err := os.WriteFile(s.path, out, 0o600); err != nil {
		return drained, remaining, fmt.Errorf("rewrite spool: %w", err)
	}
	return drained, remaining, nil
}

// Len returns the number of spooled entries.
func (s *Spool) Len() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			n++
		}
	}
	return n, nil
}
