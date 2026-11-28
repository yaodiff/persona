package node

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/rpc"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// A resident node (`personad node --attach`, run by launchd in the user's login session on a Mac) and a relay
// (`personad node --stdio`, what the hub starts over ssh) share the work: when a resident node is listening, the relay
// only pipes the ssh connection to it, and the resident node serves it. Everything the node starts (the assistant's
// claude, its key helper) then runs in the login session, where the login keychain is unlocked: an ssh session
// can't read it, so a claude whose API key comes from the keychain fails to authenticate there (docs/server/server.md).
//
// No resident node listening: --stdio serves the connection itself, as before. The hub doesn't know the difference.

// AttachSocketPath is where a resident node listens for relays.
func AttachSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.SocketDir(home), "node-attach.sock")
}

// Relay pipes r / w to a resident node if one is listening. ok is false when there is none (the caller serves the
// connection itself); otherwise it returns when either side closes.
func Relay(ctx context.Context, r io.Reader, w io.Writer) (ok bool, err error) {
	c, err := dialResident(ctx, AttachSocketPath(), residentRestartWait)
	if err != nil {
		return false, nil
	}
	defer c.Close()
	// hub -> node: when ssh's stdin ends, half-close so the node sees EOF and finishes. node -> hub decides when the
	// relay is over: everything the node wrote must reach the hub, even after the hub stopped sending.
	go func() { _, _ = io.Copy(c, r); _ = c.(*net.UnixConn).CloseWrite() }()
	back := make(chan error, 1)
	go func() { _, err := io.Copy(w, c); back <- err }()
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case err := <-back:
		return true, err
	}
}

// residentRestartWait: how long a relay waits for a resident node that is restarting (its socket is there, nobody
// answers: it is exec'ing a new binary, cmd/personad runResident) before serving the connection itself. Serving it
// here would put the assistant's conversations under this process, which ends with the hub's connection.
var residentRestartWait = 8 * time.Second

// dialResident dials the resident node's socket, again while it refuses (restarting) for up to wait; no socket at all
// (no resident node) returns at once. Once the socket has refused, a missing socket is the restarted node between
// removing the stale socket and listening on a new one (Attach), so that is waited for too.
func dialResident(ctx context.Context, path string, wait time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(wait)
	restarting := false
	for {
		c, err := net.DialTimeout("unix", path, 2*time.Second)
		restarting = restarting || errors.Is(err, syscall.ECONNREFUSED)
		again := errors.Is(err, syscall.ECONNREFUSED) || restarting && errors.Is(err, syscall.ENOENT)
		if err == nil || !again || time.Now().After(deadline) {
			return c, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// residentProgram and residentVersion are what the resident node of this process runs as (SetResident), for standby's
// answer to ping while no Node serves (spool_standby.go).
var residentProgram, residentVersion atomic.Value

// SetResident says which program and build the resident node of this process is (nodecmd runResident, before Attach).
func SetResident(program, version string) {
	residentProgram.Store(program)
	residentVersion.Store(version)
}

// Attach runs a resident node: it listens on AttachSocketPath and serves one hub connection at a time, each relayed
// by `personad node --stdio`, or arriving on extra (the hub dialing in over tailcat, tailcat.go; nil: none). A new
// connection replaces the one being served whichever way either came (the hub reconnected, maybe the other way; the
// old one may not have noticed yet), as a new ssh-started node used to replace the old one. newNode makes the Node
// for a connection. runtimes, when set, is the runtime socket (runtimehost.go): opened here, once this is the one
// resident node, and shared by every connection's Node, so runtimes connect before the hub does and stay connected
// while it reconnects.
func Attach(ctx context.Context, newNode func() (*Node, error), extra <-chan net.Conn, runtimes *RuntimeHost) error {
	transport, err := OpenResidentTransport()
	if err != nil {
		return err
	}
	defer transport.Close()
	return AttachWithTransport(ctx, newNode, extra, runtimes, transport)
}

// AttachWithTransport serves on a resident-owned listener that can retain duplicates through an executable handover.
func AttachWithTransport(ctx context.Context, newNode func() (*Node, error), extra <-chan net.Conn, runtimes *RuntimeHost, transport *ResidentTransport) error {
	ln := transport.Listener
	go func() { <-ctx.Done(); ln.Close() }()
	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "node: "+f+"\n", a...) }
	if runtimes != nil {
		if err := runtimes.Listen(ctx); err != nil {
			logf("runtime socket: %v", err)
			runtimes = nil // each connection's Node opens its own, as without a resident node
		} else {
			defer runtimes.setAgyBridge(false)
		}
	}
	relayed := make(chan net.Conn)
	go func() {
		defer close(relayed)
		acceptloop.Run(ctx, ln, logf, func(c net.Conn) {
			select {
			case relayed <- c:
			case <-ctx.Done(): // ctx closes ln too, which ends the loop
				c.Close()
			}
		})
	}()

	keeper := newAssistantKeeper()
	scans := &bgCarry{} // background-task scans go on from one connection's Node to the next (carry.go)
	// Launcher switches too: one that outlives its connection is seen, waited for and written by the next Node
	// (switch_share.go).
	switches := &switchShare{}
	// No manager connected yet (or between connections): standby answers node.sock (spool_standby.go).
	sp := openSpool(SpoolPath())
	sb := newStandby(sp, logf)
	sb.want()
	defer sb.unwant()
	var mu sync.Mutex        // served, cur, and standby's want / unwant with them
	var served chan struct{} // closed when the connection being served has ended
	var cur *attached        // the connection being served (attach_resume.go)
	var current context.CancelFunc
	var wg sync.WaitGroup
	defer keeper.stopAll() // after the last connection handed them over: this resident node is ending
	defer wg.Wait()
	// A new transport is served once its first line says what it is (attach_resume.go): read beside the loop, so one
	// that says nothing holds up no other. Connections still replace each other in the order they came: one that came
	// before the connection served, and spoke after it, is let go (an attempt the manager gave up on).
	peeked := make(chan peekedConn)
	stopped := make(chan struct{}) // this loop returned: a peek still going lets its transport go
	defer close(stopped)
	var came, servedCame uint64 // transports in the order they came; the one of the connection served
	peek := func(c net.Conn, at uint64) {
		first, rest, err := readFirstLine(c)
		select {
		case peeked <- peekedConn{c: c, came: at, first: first, rest: rest, err: err}:
		case <-ctx.Done():
			c.Close()
		case <-stopped:
			c.Close()
		}
	}
	for {
		var c net.Conn
		var ok bool
		var first []byte
		var rest io.Reader
		var at uint64 // when c came (came)
		select {
		case <-ctx.Done():
			return nil
		case c, ok = <-relayed:
			if !ok {
				return nil
			}
			came++
			go peek(c, came)
			continue
		case c, ok = <-extra:
			if !ok {
				extra = nil // that way is gone; relays go on
				continue
			}
			came++
			go peek(c, came)
			continue
		case f := <-peeked:
			c, first, rest, at = f.c, f.first, f.rest, f.came
			if f.err != nil && !errors.Is(f.err, bufio.ErrTooLong) {
				// It ended, or said nothing in time, before a whole first line: not a manager's connection (one cut
				// while asking to resume included), and it must not replace the connection kept.
				_ = c.Close()
				continue
			}
		}
		if req, ok := rpc.ParseResume(first); ok { // the manager takes its connection back (attach_resume.go)
			mu.Lock()
			a := cur
			mu.Unlock()
			var err error = rpc.ErrResumeRefused
			if a != nil {
				err = a.resume(req, c, rest)
			}
			switch {
			case err == nil:
				logf("connection resumed on a new transport")
			case errors.Is(err, rpc.ErrResumeRefused):
				_ = rpc.RefuseResume(c, req)
				_ = c.Close()
				logf("refused to resume a connection this node does not have")
			default: // the new transport failed while answering: the connection waits for the next
				_ = c.Close()
				logf("resuming on a new transport: %v", err)
			}
			continue
		}
		if at < servedCame {
			_ = c.Close()
			logf("let go a connection that came before the one served")
			continue
		}
		servedCame = at
		a := &attached{conn: c}
		reader := connReader{Reader: io.MultiReader(bytes.NewReader(first), rest), a: a}
		if current != nil {
			current() // the previous connection's node shuts down; its hooks retry on the new one (hooks.go)
		}
		cctx, cancel := context.WithCancel(ctx)
		current = cancel
		wg.Add(1)
		mu.Lock()
		prev, done := served, make(chan struct{})
		served, cur = done, a
		sb.unwant() // the new connection's Node takes node.sock; under mu, so an ending one cannot want it back
		mu.Unlock()
		began := time.Now()
		go func() {
			defer wg.Done()
			defer func() { // the last connection ended and no other came: standby until the next
				mu.Lock()
				defer mu.Unlock()
				if time.Since(began) >= time.Minute { // a connection that held (replaced or not): the hold counts from now
					sb.managerSeen(time.Now())
				}
				if served != done || ctx.Err() != nil {
					return
				}
				sb.want()
			}()
			defer close(done)
			defer a.end(errReplaced)
			defer cancel()
			if prev != nil { // the last connection's Node hands its conversations over as it ends (assistant_keep.go)
				select {
				case <-prev:
				case <-time.After(10 * time.Second):
				}
			}
			n, err := newNode()
			if err != nil {
				fmt.Fprintf(os.Stderr, "node: %v\n", err)
				return
			}
			if runtimes != nil {
				n.runtimeHost = runtimes // Serve attaches it for this connection
				n.runtimes = runtimes.reg
			}
			n.adoptBgScans(scans)
			n.share.Store(switches)
			n.keeper = keeper // conversations go on from one connection to the next (assistant_keep.go)
			n.spool = sp      // what standby took goes to this connection first (spool.go)
			// The connection outlives a transport that ends, for the manager to resume (attach_resume.go). It writes on c
			// until a resume gives its frame writer the new transport (rpc.Peer.AcceptResume).
			n.resumeWindow, n.onPeer = resumeWindow, a.setPeer
			go func() { <-cctx.Done(); a.end(errReplaced) }() // a replaced connection stops at once, a waiting one too
			if err := n.Serve(cctx, reader, c); err != nil && !errors.Is(err, io.EOF) && cctx.Err() == nil {
				n.logger.Printf("connection ended: %v", err)
			}
			n.handOverBgScans(scans)
		}()
	}
}
