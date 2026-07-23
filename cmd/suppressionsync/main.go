// Command suppressionsync is the relay-side sync hook for the suppression list. It pulls the
// active suppression export from the MX Sentinel API and writes it atomically to a file the
// relay (Postfix) consumes — either a plaintext hash list or a Postfix access(5) map.
//
// It is deliberately a small standalone binary rather than an mxctl subcommand so that it can
// be dropped onto a relay node (which need not carry the full operator CLI) and run from cron
// or a systemd timer. See INTEGRATION_bounce-suppression.md for how the same logic would be
// registered as `mxctl suppression sync` inside cmd/mxctl if colocated.
//
// Usage:
//
//	suppressionsync \
//	  -api https://sentinel.squidix.net \
//	  -token "$MXS_API_TOKEN" \
//	  -format postfix \
//	  -out /etc/postfix/mxs_suppression \
//	  [-postmap]      # run `postmap` on the output (postfix format only)
//
// The token needs the "read" scope. On any error the existing file is left untouched (fail
// closed on the artifact, so a transient API outage never wipes an in-use map).
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	api := flag.String("api", envOr("MXS_API_BASE", "http://localhost:8080"), "MX Sentinel API base URL")
	token := flag.String("token", os.Getenv("MXS_API_TOKEN"), "API token (read scope); or set MXS_API_TOKEN")
	format := flag.String("format", "plain", "export format: plain | postfix")
	out := flag.String("out", "", "output file path (required)")
	postmap := flag.Bool("postmap", false, "run `postmap` on the output (postfix format only)")
	timeout := flag.Duration("timeout", 30*time.Second, "HTTP timeout")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "suppressionsync: -out is required")
		os.Exit(2)
	}
	if *format != "plain" && *format != "postfix" {
		fmt.Fprintln(os.Stderr, "suppressionsync: -format must be plain or postfix")
		os.Exit(2)
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "suppressionsync: no API token (set -token or MXS_API_TOKEN)")
		os.Exit(2)
	}

	if err := run(*api, *token, *format, *out, *postmap, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "suppressionsync:", err)
		os.Exit(1)
	}
}

func run(api, token, format, out string, postmap bool, timeout time.Duration) error {
	url := fmt.Sprintf("%s/v1/suppression/export?format=%s", api, format)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch export: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("export returned %s: %s", resp.Status, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read export body: %w", err)
	}

	// Atomic write: temp file in the same dir, then rename — so the relay never reads a
	// half-written map.
	dir := filepath.Dir(out)
	tmp, err := os.CreateTemp(dir, ".mxs-suppression-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, out); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}

	if postmap && format == "postfix" {
		cmd := exec.Command("postmap", out)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("postmap %s: %w", out, err)
		}
	}
	fmt.Printf("suppressionsync: wrote %s (%d bytes)\n", out, len(body))
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
