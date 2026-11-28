//go:build unix

package hubedge

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Policy budgets, not measurements: how long each step of a switch may take before it counts as failed.
const (
	defaultHoldMax      = 60 * time.Second  // a connection waits this long for a slot to serve it
	defaultStandbyWait  = 90 * time.Second  // a new binary starts and checks itself (config, database)
	defaultReleaseWait  = 20 * time.Second  // the old slot stops (the hub's own stop budget is 8 s)
	defaultActivateWait = 120 * time.Second // the new slot migrates its database and listens
	defaultConfirmWait  = 3 * time.Minute   // an update's new slot confirms it (selfupdate.go: 2 minutes)
	maxWaiting          = 4096              // connections held at once (the socket unit's backlog)
	maxFailedStarts     = 5                 // the active slot dying this many times in a row ends the edge (the service manager takes over)
)

// Options is how Run runs the edge.
type Options struct {
	Listeners []net.Listener // the hub's ports, held for as long as the edge runs
	Binary    string         // the hub binary the first slot runs
	Args      []string       // a slot's arguments ("hub", "slot")
	RunDir    string         // where the slots' sockets go
	Control   string         // the control socket (`personad hub switch`)
	Version   string         // the edge's own build, for status
	Logf      func(format string, args ...any)
	// RestoreUpdate puts back what ran before the update to build (the pending marker's binaries, selfupdate.go): its
	// new slot could not take over (cause), or did not confirm it in time (cause nil). It says which binary to run
	// now; ok false when no such update is under way (confirmed, or rolled back meanwhile).
	// A restoration error is fatal: never fall back to a path whose files may be mixed.
	RestoreUpdate func(build int64, cause error) (binary string, ok bool, err error)

	// Budgets (zero: the defaults), for the tests.
	HoldMax, StandbyWait, ReleaseWait, ActivateWait, ConfirmWait time.Duration
}

type switchReq struct {
	m        Msg
	from     *proc // the active slot that asked, if one did
	progress func(phase string)
	done     chan error
}

type edge struct {
	o   Options
	ctx context.Context

	mu     sync.Mutex
	active *proc
	gate   chan struct{} // closed while connections may go to active
	held   bool
	served string // the name of the slot that served last: a new slot takes the other one

	waiting atomic.Int32
	reqs    chan switchReq

	// The update the active slot is the result of, until it confirms it.
	confirmBuild int64
	confirmBy    *time.Timer

	// Nothing serves: start retryBinary again at retry (the edge's loop only).
	failures    int
	retry       <-chan time.Time
	retryBinary string

	// onGateWait is an optional test hook invoked right before dialActive blocks on <-gate.
	onGateWait func()
}

// Run serves until ctx ends: it starts the first slot on o.Binary, relays the listeners' connections to the active
// slot, and switches slots when the active one or `hub switch` asks. It returns when the active slot keeps failing
// to start (the service manager restarts the whole service then) or ctx ends.
func Run(ctx context.Context, o Options) error {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	orDefault(&o.HoldMax, defaultHoldMax)
	orDefault(&o.StandbyWait, defaultStandbyWait)
	orDefault(&o.ReleaseWait, defaultReleaseWait)
	orDefault(&o.ActivateWait, defaultActivateWait)
	orDefault(&o.ConfirmWait, defaultConfirmWait)
	if err := os.MkdirAll(o.RunDir, 0o700); err != nil {
		return err
	}
	e := &edge{o: o, ctx: ctx, gate: make(chan struct{}), held: true, reqs: make(chan switchReq)}
	e.logf("hub edge %s: holding %d port(s), starting the hub (%s)", o.Version, len(o.Listeners), o.Binary)
	p, err := e.startActive(o.Binary)
	if err != nil {
		return fmt.Errorf("hub edge: the hub did not start: %w", err)
	}
	e.setActive(p)
	for _, ln := range o.Listeners {
		go e.accept(ln)
	}
	ctl, err := e.listenControl()
	if err != nil {
		e.logf("hub edge: control socket: %v (hub switch is not available)", err)
	} else {
		defer ctl.Close()
	}
	return e.supervise()
}

func orDefault(d *time.Duration, def time.Duration) {
	if *d <= 0 {
		*d = def
	}
}

func (e *edge) logf(format string, args ...any) { e.o.Logf(format, args...) }

// acceptLogf is what acceptloop.Run logs about the edge's sockets (its ports, the control socket).
func (e *edge) acceptLogf(format string, args ...any) { e.logf("hub edge: "+format, args...) }
