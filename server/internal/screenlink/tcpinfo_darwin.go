package screenlink

import (
	"net"

	"golang.org/x/sys/unix"
)

// tcpPackets is a TCP connection's packets sent and retransmitted so far (TCP_CONNECTION_INFO); ok false when unknown.
func tcpPackets(c *net.TCPConn) (sent, retrans uint64, ok bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	_ = raw.Control(func(fd uintptr) {
		info, err := unix.GetsockoptTCPConnectionInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_CONNECTION_INFO)
		if err == nil {
			sent, retrans, ok = info.Txpackets, info.Txretransmitpackets, true
		}
	})
	return sent, retrans, ok
}
