package osproctest

import "golang.org/x/sys/unix"

// isSystemSocket reports whether fd is the resolver's AF_SYSTEM kernel control socket. Darwin has no SO_DOMAIN
// getsockopt, and getsockname is unsupported on this socket; CTLIOCGINFO identifies its registered control name.
func isSystemSocket(fd int) bool {
	var info unix.CtlInfo
	copy(info.Name[:], "com.apple.netsrc")
	return unix.IoctlCtlInfo(fd, &info) == nil
}
