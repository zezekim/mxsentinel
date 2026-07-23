//go:build !linux

package cpanelplugin

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

// peerUID is a development stub for non-Linux platforms (the plugin only ships on
// Linux cPanel hosts). To exercise the broker locally, set MXS_PLUGIN_FAKE_UID to
// the uid the broker should attribute to incoming connections.
func peerUID(net.Conn) (uint32, error) {
	if v := os.Getenv("MXS_PLUGIN_FAKE_UID"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("MXS_PLUGIN_FAKE_UID: %w", err)
		}
		return uint32(n), nil
	}
	return 0, fmt.Errorf("peerUID: SO_PEERCRED is only supported on linux")
}
