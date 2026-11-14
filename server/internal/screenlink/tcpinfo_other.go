//go:build !darwin && !linux

package screenlink

import "net"

// tcpPackets is unknown here: pongs carry 0, 0.
func tcpPackets(*net.TCPConn) (sent, retrans uint64, ok bool) { return 0, 0, false }
