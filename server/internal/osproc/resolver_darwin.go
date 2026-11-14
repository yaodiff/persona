package osproc

import (
	"context"
	"net"
	"time"
)

// resolverWarmupTimeout bounds the one local lookup at process start. The usual cost is a few milliseconds.
const resolverWarmupTimeout = 50 * time.Millisecond

// resolverLookupHost is the default resolver, including the user's GODEBUG=netdns choice; tests inject a slow lookup.
var resolverLookupHost = net.DefaultResolver.LookupHost

// warmResolver makes macOS's system resolver open its persistent AF_SYSTEM control socket before openFDs seals the
// process's descriptors. Looking up localhost needs no DNS server. The pure Go resolver opens no such socket, and its
// selection is left untouched. Errors are irrelevant to sealing and never stop the caller.
//
// The context bounds the wait. If the deadline passes, the underlying system lookup may finish later and open the
// socket after the seal; children can then inherit it as before this warm-up, rather than startup waiting any longer.
func warmResolver() {
	ctx, cancel := context.WithTimeout(context.Background(), resolverWarmupTimeout)
	defer cancel()
	_, _ = resolverLookupHost(ctx, "localhost")
}
