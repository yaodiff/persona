package screenlink

import (
	"net"

	"golang.org/x/sys/unix"
)

// tcpPackets is a TCP connection's segments sent and retransmitted so far (TCP_INFO); ok false when unknown.
func tcpPackets(c *net.TCPConn) (sent, retrans uint64, ok bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	_ = raw.Control(func(fd uintptr) {
		info, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
		if err == nil {
			sent, retrans, ok = uint64(info.Segs_out), uint64(info.Total_retrans), true
		}
	})
	return sent, retrans, ok
}
