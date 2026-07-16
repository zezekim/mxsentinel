//go:build linux

package cpanelplugin

import (
	"fmt"
	"net"
	"syscall"
)

// peerUID returns the uid of the process on the other end of a unix-socket
// connection, as reported by the kernel (SO_PEERCRED). This cannot be forged by
// the client, which is what lets the broker trust it for per-account scoping.
func peerUID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("peerUID: connection is not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	return cred.Uid, nil
}
